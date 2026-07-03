## Your network runs through Popo's gated proxy — be patient

You have **no direct internet**: all HTTPS routes through Popo's **gated proxy**
(`HTTPS_PROXY` + CA are preset). Native tools — `pip`/`npm`/`git`/`curl` — work through it
(lockfiles, `npm ci`, cloning). But **runtimes** still come from `popo <lang>.install`, and
a **new host may be held for operator approval** — if a fetch stalls, relay the message,
then rerun it. There is **no fallback** beyond Popo; don't route around it.
