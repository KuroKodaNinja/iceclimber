# Java application scenario

A self-contained, full-stack scenario that builds and runs a real **Java** program
in the sandbox, the way the agent would — the JVM counterpart of the Node scenario.

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

Run it (needs the Lima sandbox up):

```sh
make scenario            # runs every test/scenarios/<lang> scenario
# or just this one:
go test -tags scenario -run TestJavaApp ./test/scenarios/java/...
```

`app/App.java` is the application; everything else is the harness driving the real
`iceclimber` binary against the VM. Tier 1 relay is covered by the functional suite
(`make tui-functional`), not here, since this host may lack a controller JDK.
