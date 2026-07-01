# Java application scenarios

Two self-contained JVM scenarios that build and run real **Java** programs in the
sandbox, the way the agent would.

## `TestJavaApp` — Maven-coordinate resolution → classpath

What it exercises, end to end:

1. **`web.fetch` through Popo** — fetches the xkcd comic JSON (sandbox venue).
2. **`java.install`** — provisions a portable Temurin JDK.
3. **`maven.install`** — resolves **Gson** (`com.google.code.gson:gson`) and its
   transitive deps into a classpath via Coursier (Tier 0, against Maven Central).
4. **compile + run** — `javac` builds `app/App.java`, which uses Gson to parse the
   fetched comic and print a computed one-line report; `java -cp <classpath>` runs it.
5. **assert** — the report carries the computed values (comic number, title, and the
   UTF-16 title length), proving the program ran, Gson loaded, and it processed the
   fetched data.

## `TestJavaMavenBuild` — the Maven build tool, in the sandbox, air-gapped

Runs `mvn` **inside the sandbox** on a real `pom.xml` project (`mvnapp/`, with a Gson
dependency), with no sandbox network:

1. **`web.fetch`** — same comic input.
2. **`java.install`** — a portable Temurin JDK (JAVA_HOME for the build).
3. **`maven build` (air-gapped)** — the controller's Maven+JDK prime an offline `.m2`
   repo by actually building the project (resolving every dep + plugin), then Popo
   relays the Maven tool + that repo in, and the sandbox runs **`mvn -o package`**
   offline, producing `target/xkcdtool.jar`.
4. **run** — the built jar runs with Gson from the relayed offline repo, parsing the
   comic and printing `MAVEN_BUILD_OK` + the computed report.
5. **assert** — output carries `MAVEN_BUILD_OK`, the comic number, title, and UTF-16
   title length. Skips without Maven + a JDK on the controller (the prime engine),
   mirroring the conda relay's controller-conda dependency.

## Running

```sh
make scenario            # runs every test/scenarios/<lang> scenario
# or just these:
go test -tags scenario -run 'TestJavaApp|TestJavaMavenBuild' ./test/scenarios/java/...
```

`app/App.java` and `mvnapp/` are the applications; everything else is the harness
driving the real `iceclimber` binary against the VM. (`maven.install` Tier 1 relay is
covered by `make tui-functional`.)
