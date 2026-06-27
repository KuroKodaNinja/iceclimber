package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
	"github.com/spf13/cobra"
)

func newWebCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Fetch URLs from the sandbox's network position",
	}
	cmd.AddCommand(newWebFetchCmd())
	return cmd
}

func newWebFetchCmd() *cobra.Command {
	var transport, method, data string
	var headers []string
	cmd := &cobra.Command{
		Use:   "fetch <url>",
		Short: "Fetch a URL over the sandbox's own egress (sandbox-exec venue)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			req := webfetch.Request{URL: args[0], Method: method, Headers: parseHeaderFlags(headers)}
			if data != "" {
				req.Body = &data
			}
			res, err := webfetch.Run(ctx, webfetchDeps(sess), "", req)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%d  (venue %s)\n", res.StatusCode, res.Venue)
			switch {
			case res.BodyBlob != "":
				fmt.Fprintf(w, "body: %s (%d bytes)\n", res.BodyBlob, res.BodySize)
			case res.Encoding == "base64":
				fmt.Fprintf(w, "body: %d bytes, base64 (sha %s)\n", res.BodySize, res.BodySHA256[:12])
			default:
				fmt.Fprintln(w, snippet(res.BodyInline, 600))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&method, "method", "GET", "HTTP method")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "request header 'Key: Value' (repeatable)")
	cmd.Flags().StringVar(&data, "data", "", "request body")
	return cmd
}

func parseHeaderFlags(hs []string) map[string]string {
	m := make(map[string]string, len(hs))
	for _, h := range hs {
		if k, v, ok := strings.Cut(h, ":"); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func snippet(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
