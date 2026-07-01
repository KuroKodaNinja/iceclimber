// Package runtimes models the operator's choice of where each language runtime
// comes from — iceclimber-managed (relay-install a pinned build) vs a pre-existing
// system runtime already on the sandbox (brownfield) — and persists that choice
// controller-side. It only records intent; the env strategy that acts on it (venv,
// conda, …) lives with the installers.
package runtimes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Mode is where a language runtime comes from.
type Mode string

const (
	ModeManaged Mode = "managed" // iceclimber relay-installs a pinned build (the default)
	ModeSystem  Mode = "system"  // use a runtime already present on the sandbox
)

// Langs are the languages a source can be chosen for, in display order.
var Langs = []string{"python", "node", "java"}

// SystemSupported reports whether ModeSystem (use a pre-existing runtime) is
// implemented for lang. Only python (venv) is wired today; node/java system mode is
// future work, so we reject it at choose-time rather than persist a no-op that fails
// confusingly at install.
func SystemSupported(lang string) bool { return lang == "python" }

// Source is the chosen origin for one language's runtime.
type Source struct {
	Mode Mode `json:"mode"`
	// Path optionally pins the system interpreter (else discovery's PATH result is used).
	Path string `json:"path,omitempty"`
	// EnvManager selects the isolation tool for system mode (python: "venv"|"conda").
	EnvManager string `json:"env_manager,omitempty"`
}

// Sources maps a language to its chosen Source. A language absent from the map is
// implicitly ModeManaged (today's behavior).
type Sources map[string]Source

// Of returns the source for lang, defaulting to managed when unset.
func (s Sources) Of(lang string) Source {
	if src, ok := s[lang]; ok && src.Mode != "" {
		return src
	}
	return Source{Mode: ModeManaged}
}

// validMode reports whether m is a recognized mode (empty is treated as unset).
func validMode(m Mode) bool { return m == ModeManaged || m == ModeSystem }

// ParseFlag parses a `--runtime-source` value: a comma-separated list of lang=mode
// pairs, e.g. "python=system,node=managed". An empty string yields an empty Sources.
// Unknown langs/modes (or system mode for an unsupported lang) are an error. A
// system interpreter path is set in config, not on the flag.
func ParseFlag(s string) (Sources, error) {
	out := Sources{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("runtime-source %q must be lang=mode", part)
		}
		lang := strings.TrimSpace(k)
		if !knownLang(lang) {
			return nil, fmt.Errorf("runtime-source: unknown language %q (want one of %s)", lang, strings.Join(Langs, ", "))
		}
		modeStr, mgr, _ := strings.Cut(strings.TrimSpace(v), ":") // "system:conda" → mode + env_manager
		mode := Mode(strings.TrimSpace(modeStr))
		if !validMode(mode) {
			return nil, fmt.Errorf("runtime-source %s: unknown mode %q (want managed or system, optionally :venv/:conda)", lang, v)
		}
		if mode == ModeSystem && !SystemSupported(lang) {
			return nil, fmt.Errorf("runtime-source %s=system is not supported yet (only python); use managed", lang)
		}
		src := Source{Mode: mode}
		if mgr = strings.TrimSpace(mgr); mgr != "" {
			if mode != ModeSystem {
				return nil, fmt.Errorf("runtime-source %s: env_manager (:%s) only applies to system mode", lang, mgr)
			}
			if lang != "python" {
				return nil, fmt.Errorf("runtime-source %s: only python has an env_manager", lang)
			}
			if mgr != "venv" && mgr != "conda" {
				return nil, fmt.Errorf("runtime-source %s: env_manager %q must be venv or conda", lang, mgr)
			}
			src.EnvManager = mgr
		}
		out[lang] = src
	}
	return out, nil
}

func knownLang(lang string) bool {
	for _, l := range Langs {
		if l == lang {
			return true
		}
	}
	return false
}

// Resolve merges the layered choices by precedence — flag > config > persisted —
// and fills the rest via prompt (when non-nil and a runtime was detected for that
// lang) or the managed default. prompt is called only for a language with no
// explicit choice in any layer AND a non-empty detected entry; returning a zero
// (empty Mode) Source from prompt keeps the managed default. detected maps a lang to
// whether a system runtime was discovered.
func Resolve(flag, cfg, persisted Sources, detected map[string]bool, prompt func(lang string) Source) Sources {
	out := Sources{}
	for _, lang := range Langs {
		switch {
		case has(flag, lang):
			out[lang] = flag[lang]
		case has(cfg, lang):
			out[lang] = cfg[lang]
		case has(persisted, lang):
			out[lang] = persisted[lang]
		case prompt != nil && detected[lang]:
			if s := prompt(lang); s.Mode != "" {
				out[lang] = s
			} else {
				out[lang] = Source{Mode: ModeManaged}
			}
		default:
			out[lang] = Source{Mode: ModeManaged}
		}
	}
	return out
}

func has(s Sources, lang string) bool {
	src, ok := s[lang]
	return ok && src.Mode != ""
}

// Summary renders the resolved sources as a stable, human one-liner per language.
func (s Sources) Summary() string {
	var b strings.Builder
	langs := make([]string, 0, len(s))
	for l := range s {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for i, l := range langs {
		if i > 0 {
			b.WriteString(", ")
		}
		src := s[l]
		fmt.Fprintf(&b, "%s=%s", l, src.Mode)
		if src.EnvManager != "" {
			fmt.Fprintf(&b, "(%s)", src.EnvManager)
		}
	}
	return b.String()
}

// Store persists Sources as JSON at a controller-side path (per sandbox).
type Store struct{ path string }

// NewStore addresses the runtimes file at path.
func NewStore(path string) *Store { return &Store{path: path} }

// Load reads the persisted sources; a missing file yields empty Sources, not an error.
func (s *Store) Load() (Sources, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return Sources{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read runtimes %s: %w", s.path, err)
	}
	var out Sources
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse runtimes %s: %w", s.path, err)
	}
	if out == nil {
		out = Sources{}
	}
	return out, nil
}

// Save writes the sources (pretty JSON, 0600), creating the parent dir. The write
// is atomic (temp + rename) so a concurrent Load — e.g. an install resolving the
// source while the console persists a new choice — never sees a partial file.
func (s *Store) Save(src Sources) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
