# Changelog

## [0.19.0](https://github.com/suzworx/flywheel/compare/v0.18.2...v0.19.0) (2026-09-24)


### Features

* a quiet gate waits for an idle host and holds dispatch while it runs ([#413](https://github.com/suzworx/flywheel/issues/413)) ([57882fa](https://github.com/suzworx/flywheel/commit/57882fa7f40739d9532000ed6400d195f502ad3f)), closes [#411](https://github.com/suzworx/flywheel/issues/411)
* an independent review agent reads a unit's diff and records findings ([#399](https://github.com/suzworx/flywheel/issues/399)) ([6cef732](https://github.com/suzworx/flywheel/commit/6cef732f103fcdafea6ab31d96574618d3f33596))
* flywheel commits each attempt on the unit's branch; workers never touch index or history ([#404](https://github.com/suzworx/flywheel/issues/404)) ([12d6682](https://github.com/suzworx/flywheel/commit/12d6682b00cf5e291dab98324cb2490feb80176a)), closes [#391](https://github.com/suzworx/flywheel/issues/391)
* flywheel wait and run --notify, so a finished unit always reaches the lead ([#403](https://github.com/suzworx/flywheel/issues/403)) ([f2dea2a](https://github.com/suzworx/flywheel/commit/f2dea2a0da338dbb03d62d2814335f4339ef976c)), closes [#393](https://github.com/suzworx/flywheel/issues/393)
* learnings carry a scope, and a journal line is a note, never a learning ([#412](https://github.com/suzworx/flywheel/issues/412)) ([21c555a](https://github.com/suzworx/flywheel/commit/21c555a6adbdad9e79a1b95d3fdbdad6622df348)), closes [#409](https://github.com/suzworx/flywheel/issues/409)
* owns supports negated entries so an exception is machine-checked ([#405](https://github.com/suzworx/flywheel/issues/405)) ([9d24f07](https://github.com/suzworx/flywheel/commit/9d24f07408c269112a0a9e04852ca1e62506bcab)), closes [#388](https://github.com/suzworx/flywheel/issues/388)
* the floor shows each unit's worktree and base; --worktree is the parallel default; corrections cite evidence ([#407](https://github.com/suzworx/flywheel/issues/407)) ([e11c4c0](https://github.com/suzworx/flywheel/commit/e11c4c08db1e5e45cbd7e9bfb036b0c1fa08659e)), closes [#394](https://github.com/suzworx/flywheel/issues/394)
* the review loop sends open findings back to the worker, who must answer each by id ([#410](https://github.com/suzworx/flywheel/issues/410)) ([d70417e](https://github.com/suzworx/flywheel/commit/d70417eb9e87a3841d2bd4f305cd9e87661fa887))


### Bug Fixes

* a flywheel command run inside a unit's worktree uses the main ledger ([#398](https://github.com/suzworx/flywheel/issues/398)) ([38cc486](https://github.com/suzworx/flywheel/commit/38cc486f6cafb9ba1fae7d1a022e696110163ce6)), closes [#395](https://github.com/suzworx/flywheel/issues/395)
* a missing flag in flywheel log prints the error and the fixed command, not the whole usage ([#397](https://github.com/suzworx/flywheel/issues/397)) ([e98e8c2](https://github.com/suzworx/flywheel/commit/e98e8c2ea584f8820ec73cf2470dafb6a6bfca0a)), closes [#392](https://github.com/suzworx/flywheel/issues/392)
* a worker that ends its session while its background job runs is abandoned-job, and is resumed ([#406](https://github.com/suzworx/flywheel/issues/406)) ([2c7d884](https://github.com/suzworx/flywheel/commit/2c7d884ef872605b39b3874e2f30809068ecaf10)), closes [#390](https://github.com/suzworx/flywheel/issues/390)
* an abandoned attempt is marked lost and never blocks dispatch ([#408](https://github.com/suzworx/flywheel/issues/408)) ([00894fd](https://github.com/suzworx/flywheel/commit/00894fdbf877ffe78703a7e948546d6bad111502)), closes [#402](https://github.com/suzworx/flywheel/issues/402)
* another unit's run --worktree worktree is attributed to that unit, not blamed on this one ([#396](https://github.com/suzworx/flywheel/issues/396)) ([b065b1f](https://github.com/suzworx/flywheel/commit/b065b1ffed758af3e6cefe7fc0e1af20adbd9cad)), closes [#386](https://github.com/suzworx/flywheel/issues/386)
* brief-drift advice names the step that takes effect on a dispatched attempt ([#401](https://github.com/suzworx/flywheel/issues/401)) ([3af035f](https://github.com/suzworx/flywheel/commit/3af035f822298c310c02e3bddb8ec3b39975547a)), closes [#387](https://github.com/suzworx/flywheel/issues/387)

## [0.18.2](https://github.com/suzworx/flywheel/compare/v0.18.1...v0.18.2) (2026-09-24)


### Bug Fixes

* a rate-limited model is paused until the reset, and the floor says until when ([#384](https://github.com/suzworx/flywheel/issues/384)) ([14f060f](https://github.com/suzworx/flywheel/commit/14f060fc235371c3c5fd1a5f810c092154f6645f)), closes [#383](https://github.com/suzworx/flywheel/issues/383)

## [0.18.1](https://github.com/suzworx/flywheel/compare/v0.18.0...v0.18.1) (2026-09-24)


### Bug Fixes

* a rate-limited worker is recorded as rate-limited and resumed after the reset ([#381](https://github.com/suzworx/flywheel/issues/381)) ([6861729](https://github.com/suzworx/flywheel/commit/68617290a140274dc76ba9f0909a3d7ac6e170ee)), closes [#380](https://github.com/suzworx/flywheel/issues/380)

## [0.18.0](https://github.com/suzworx/flywheel/compare/v0.17.0...v0.18.0) (2026-09-24)


### Features

* a circuit breaker stops dispatching to a model that keeps failing on provider errors ([#304](https://github.com/suzworx/flywheel/issues/304)) ([7ed0312](https://github.com/suzworx/flywheel/commit/7ed03124f902a9349a28b60f7f1c7d7d67667330))
* a codex adapter dispatches OpenAI Codex CLI workers through codex exec --json ([#312](https://github.com/suzworx/flywheel/issues/312)) ([3ff377f](https://github.com/suzworx/flywheel/commit/3ff377f81834e521ca85d698914bc7e23ac8a481))
* a git guard on the worker's PATH refuses history-writing git commands, whatever the adapter ([#325](https://github.com/suzworx/flywheel/issues/325)) ([413366a](https://github.com/suzworx/flywheel/commit/413366ad1c7038be461f4ef9417aebb32504471b))
* a passing flywheel doctor --record probe closes a model's breaker ([#334](https://github.com/suzworx/flywheel/issues/334)) ([8665c57](https://github.com/suzworx/flywheel/commit/8665c571c32a20da0daf4202c2f97333dd412a90))
* a standard-library terminal layer for the interactive factory, and live factory on Windows ([#341](https://github.com/suzworx/flywheel/issues/341)) ([d4239b9](https://github.com/suzworx/flywheel/commit/d4239b9b4c3dfd0a8297f7577ab5f3efc0180200))
* a tamper-evident event log — a hash chain checked by flywheel verify --log ([#299](https://github.com/suzworx/flywheel/issues/299)) ([7af5f48](https://github.com/suzworx/flywheel/commit/7af5f48c53d484a42fcf59631e2952e8f0a99986))
* a token budget and a per-model dispatch rate limit ([#329](https://github.com/suzworx/flywheel/issues/329)) ([ad7105b](https://github.com/suzworx/flywheel/commit/ad7105b93ab3c16c8bde7658a30932434af7b8c2))
* an approved fallback takes over while the default model's breaker is open ([#321](https://github.com/suzworx/flywheel/issues/321)) ([7e4c698](https://github.com/suzworx/flywheel/commit/7e4c698b211f406114122dcbb89de8272335c86c))
* claim-edit --worktree attributes a sibling worktree's edit to another session ([#377](https://github.com/suzworx/flywheel/issues/377)) ([37dfc79](https://github.com/suzworx/flywheel/commit/37dfc79715e04391f2eafba50255876bc52556d3)), closes [#362](https://github.com/suzworx/flywheel/issues/362)
* flywheel attest accepts a gate reading measured outside flywheel ([#378](https://github.com/suzworx/flywheel/issues/378)) ([b6bd3a6](https://github.com/suzworx/flywheel/commit/b6bd3a6c5ee64f5394de9756973759571dc17865)), closes [#367](https://github.com/suzworx/flywheel/issues/367)
* flywheel audit --sample and --first-article select which units to audit ([#309](https://github.com/suzworx/flywheel/issues/309)) ([3915fbd](https://github.com/suzworx/flywheel/commit/3915fbd67224910b2ce71f09bac2b57a09a6d4c8))
* flywheel audit --wave audits every passed, unaudited unit in the ledger ([#323](https://github.com/suzworx/flywheel/issues/323)) ([4e9650a](https://github.com/suzworx/flywheel/commit/4e9650aa885ee19ef6bc6a345e1d8b48d1314354))
* flywheel audit re-measures a unit independently and records audited ([#301](https://github.com/suzworx/flywheel/issues/301)) ([ae96bd9](https://github.com/suzworx/flywheel/commit/ae96bd9948b8117866afb2865eb7ec15fdde9684))
* flywheel context --role gives each role only its own open work ([#302](https://github.com/suzworx/flywheel/issues/302)) ([0564785](https://github.com/suzworx/flywheel/commit/0564785cf24b14262e4f10f47cfd83572f36d6ee))
* flywheel context gives a joining agent the factory's state in one read ([#297](https://github.com/suzworx/flywheel/issues/297)) ([6b9e2c3](https://github.com/suzworx/flywheel/commit/6b9e2c3bf613b8f6d59349e51503d57deb646a9a))
* flywheel doctor --worker checks a local model's endpoint directly ([#330](https://github.com/suzworx/flywheel/issues/330)) ([4551f8f](https://github.com/suzworx/flywheel/commit/4551f8fb4131a861e0094732e2cc0506c20566e6))
* flywheel explain tells one unit's whole story from the ledger ([#283](https://github.com/suzworx/flywheel/issues/283)) ([34c90c3](https://github.com/suzworx/flywheel/commit/34c90c33719196d780e7da81c50268d7c5efcc59))
* flywheel factory is interactive in a terminal — k9s-style views, filter, drill-down, a lines view ([#346](https://github.com/suzworx/flywheel/issues/346)) ([c1d7781](https://github.com/suzworx/flywheel/commit/c1d7781e329960470f8d1bbc6835febbac70282f))
* flywheel gate exits 6 while work is left unjudged ([#291](https://github.com/suzworx/flywheel/issues/291)) ([38e8579](https://github.com/suzworx/flywheel/commit/38e8579bdec58ef6c3166006d2f726c279ee4487))
* flywheel init --ci writes a flywheel-audit job that verifies the records on every PR ([#315](https://github.com/suzworx/flywheel/issues/315)) ([db13d3b](https://github.com/suzworx/flywheel/commit/db13d3be0532e4560fc200387869ff608d3af3fa))
* flywheel init --git-hooks enforces a Flywheel-Task trailer and verifies units on push ([#308](https://github.com/suzworx/flywheel/issues/308)) ([89683f5](https://github.com/suzworx/flywheel/commit/89683f51829c72aab639ed3e10fecc547ac0ef1e))
* flywheel init --local points OpenCode workers at a local model, offline ([#327](https://github.com/suzworx/flywheel/issues/327)) ([ac498b1](https://github.com/suzworx/flywheel/commit/ac498b1d87e6eda9c3c3e23634dd917b1c082d51))
* flywheel init ends with a factory summary ([#328](https://github.com/suzworx/flywheel/issues/328)) ([0764bfa](https://github.com/suzworx/flywheel/commit/0764bfa15276cf2e3484b72746d8d53b1017c809))
* flywheel land --merge lands a unit from its worktree through a local queue ([#345](https://github.com/suzworx/flywheel/issues/345)) ([d041b78](https://github.com/suzworx/flywheel/commit/d041b7894e42e0a51e20fb7bba0ab60c4e1ae642))
* flywheel log --shard switches a repository to the sharded event log ([#47](https://github.com/suzworx/flywheel/issues/47), unit 4) ([#351](https://github.com/suzworx/flywheel/issues/351)) ([6431d4c](https://github.com/suzworx/flywheel/commit/6431d4cc111a67e16a3830d5671664d5f1152bcc))
* flywheel next holds dispatches while the budget is spent or the model's breaker is open ([#311](https://github.com/suzworx/flywheel/issues/311)) ([ef8491e](https://github.com/suzworx/flywheel/commit/ef8491e5418f6868890462f5dde79bbd48db89c9))
* flywheel run --increment N dispatches one increment of a brief as a fresh session ([#295](https://github.com/suzworx/flywheel/issues/295)) ([b7b4353](https://github.com/suzworx/flywheel/commit/b7b4353d3ba9280f53a2712266f9c7f0dd84b00a))
* flywheel run --worktree runs each task in its own git worktree on branch fw/&lt;task&gt; ([#333](https://github.com/suzworx/flywheel/issues/333)) ([bd25089](https://github.com/suzworx/flywheel/commit/bd25089958447b5818d2029f6bc3fd8f9816a758))
* flywheel run flags a worker that wrote git history, whatever its adapter (git-write) ([#318](https://github.com/suzworx/flywheel/issues/318)) ([94216f3](https://github.com/suzworx/flywheel/commit/94216f30096205ed53edc78628482a13f05fda62))
* flywheel supervise measures every finished unit nobody has measured yet ([#298](https://github.com/suzworx/flywheel/issues/298)) ([48caa6e](https://github.com/suzworx/flywheel/commit/48caa6e1223188cddd8494f81a40a837f6e250ca))
* flywheel watch streams the factory's events as readable lines ([#305](https://github.com/suzworx/flywheel/issues/305)) ([51e21be](https://github.com/suzworx/flywheel/commit/51e21be40c8fb6c80e632200d35bf2875d4be66d))
* init --hooks installs a Claude Code Stop hook that runs flywheel gate ([#296](https://github.com/suzworx/flywheel/issues/296)) ([1e1fb25](https://github.com/suzworx/flywheel/commit/1e1fb2554c0c0efac8cee3614f48b2c154350de4))
* land a hand-verified unit on a recorded exception ([#277](https://github.com/suzworx/flywheel/issues/277)) ([248c6aa](https://github.com/suzworx/flywheel/commit/248c6aafeefda22d17c62ad36bfbdb99ca1406e6))
* land refuses while the task has untriaged signals, unless --allow-untriaged records why ([#287](https://github.com/suzworx/flywheel/issues/287)) ([24611e1](https://github.com/suzworx/flywheel/commit/24611e127e53d7ef081374cc71409a41741249f7))
* make go install work by declaring the real module path ([#273](https://github.com/suzworx/flywheel/issues/273)) ([863ab73](https://github.com/suzworx/flywheel/commit/863ab7394a98511eee5c2b62cf840f382e686e3d))
* measure spend against a frontier-only baseline in flywheel stats ([#292](https://github.com/suzworx/flywheel/issues/292)) ([98401f4](https://github.com/suzworx/flywheel/commit/98401f4f9874ecefc1f30798791d994395152e00))
* product lines — config lines, a brief's line:, and run staffing a unit from its line ([#340](https://github.com/suzworx/flywheel/issues/340)) ([59e00b0](https://github.com/suzworx/flywheel/commit/59e00b0d90b3f85ce406161a0e306874264bbd08))
* read a sharded event log — per-task shards merged in a deterministic order ([#47](https://github.com/suzworx/flywheel/issues/47), unit 1) ([#347](https://github.com/suzworx/flywheel/issues/347)) ([b7a9c4e](https://github.com/suzworx/flywheel/commit/b7a9c4eac649f052274e38c9a9a3a37552504bfe))
* record the commands a worker ran, and name the gates it never ran ([#379](https://github.com/suzworx/flywheel/issues/379)) ([d260286](https://github.com/suzworx/flywheel/commit/d2602866b00e135f540678748cac61843161e481)), closes [#365](https://github.com/suzworx/flywheel/issues/365)
* seal the legacy log on migration and verify every shard's chain ([#47](https://github.com/suzworx/flywheel/issues/47), unit 2b) ([#349](https://github.com/suzworx/flywheel/issues/349)) ([cb9dda1](https://github.com/suzworx/flywheel/commit/cb9dda1f5bdd4812fef88a08d9d68c50bcce5607))
* stations — where each unit stands on its production line ([#358](https://github.com/suzworx/flywheel/issues/358)) ([fdd9651](https://github.com/suzworx/flywheel/commit/fdd965195fae3090bb85a6ab692a448529a78646))
* supervise re-measures a passed unit whose owned files changed after its reading ([#303](https://github.com/suzworx/flywheel/issues/303)) ([d3a1e60](https://github.com/suzworx/flywheel/commit/d3a1e6057bc9632f479a9ba1450f1bb698f591e6))
* T7 — flywheel land refuses a unit until its worker line's first article is audited conforming ([#317](https://github.com/suzworx/flywheel/issues/317)) ([f92c6ea](https://github.com/suzworx/flywheel/commit/f92c6eaa7e8823f4c51852b5208dbd54c7ca1701))
* tail and watch a sharded log incrementally, with a process read cache ([#47](https://github.com/suzworx/flywheel/issues/47), unit 3) ([#350](https://github.com/suzworx/flywheel/issues/350)) ([199b364](https://github.com/suzworx/flywheel/commit/199b364fcf82c9f81ac042f68af47a9b54d8be98))
* the factory floor by product line — each unit's line, a lines section and a LINE column ([#69](https://github.com/suzworx/flywheel/issues/69)) ([#342](https://github.com/suzworx/flywheel/issues/342)) ([252d1d2](https://github.com/suzworx/flywheel/commit/252d1d2297a396a657ec9fbe9939735f0773ac39))
* the factory's staffing in config — lead, inspector, auditor, with the independence rule checked ([#354](https://github.com/suzworx/flywheel/issues/354)) ([a4fb86b](https://github.com/suzworx/flywheel/commit/a4fb86b8d2f1ab7121cb5cadff11603c7a900bf1))
* the handoff carries untriaged signals forward to the next head ([#294](https://github.com/suzworx/flywheel/issues/294)) ([7398c7f](https://github.com/suzworx/flywheel/commit/7398c7f58b0e63042e348be92bd10e1d5434b265))
* the k9s-style factory model — views, cursor, command bar, filter, drill-down ([#344](https://github.com/suzworx/flywheel/issues/344)) ([4f49fb3](https://github.com/suzworx/flywheel/commit/4f49fb36ad83b45a12f9bdee6fe8c5b3a84ee91e))
* the live floor shows the configured roles, and a floor that disagrees raises an andon ([#357](https://github.com/suzworx/flywheel/issues/357)) ([2cfb03b](https://github.com/suzworx/flywheel/commit/2cfb03b7efe393fd752540e5632c98efdd8fb14a))
* verify a pass measured in an external workdir, and report inconclusive ([#270](https://github.com/suzworx/flywheel/issues/270)) ([e7d80d6](https://github.com/suzworx/flywheel/commit/e7d80d68c3bb4dae05544d28fbc3e4b712ee9873))
* write a sharded event log — routing, stamping and a chain per shard ([#47](https://github.com/suzworx/flywheel/issues/47), unit 2a) ([#348](https://github.com/suzworx/flywheel/issues/348)) ([0276f5a](https://github.com/suzworx/flywheel/commit/0276f5a4aca363c04b5fb68a94a9827506a33093))


### Bug Fixes

* a sibling worktree's edits belong to any dispatched unit there that has not landed ([#282](https://github.com/suzworx/flywheel/issues/282)) ([64b776d](https://github.com/suzworx/flywheel/commit/64b776de6ce84af69399076fe8206d41374639a2))
* a sibling worktree's own lead claims excuse its changed paths in the owns check ([#343](https://github.com/suzworx/flywheel/issues/343)) ([ab9d69e](https://github.com/suzworx/flywheel/commit/ab9d69ef799aefdbeadf818d77844cb096352887))
* a worker that stops without writing, or on a permission denial, is not done ([#375](https://github.com/suzworx/flywheel/issues/375)) ([73c15c9](https://github.com/suzworx/flywheel/commit/73c15c9f91b7c473bb762235d20ae50a4ce963da)), closes [#364](https://github.com/suzworx/flywheel/issues/364)
* an inconclusive gate names the in-flight unit that owns the blocking files ([#376](https://github.com/suzworx/flywheel/issues/376)) ([2dd2bab](https://github.com/suzworx/flywheel/commit/2dd2bab100add13dc8b58e527a517f037c23f82f))
* an owns amendment after a fresh dispatch takes effect, and a narrowing is refused ([#285](https://github.com/suzworx/flywheel/issues/285)) ([02ced2e](https://github.com/suzworx/flywheel/commit/02ced2e3db77bbc367a5159de44925587c890c76))
* claude workers get the PLAN rule as a system prompt ([#371](https://github.com/suzworx/flywheel/issues/371)) ([c5e5c13](https://github.com/suzworx/flywheel/commit/c5e5c133123f3bd0eb5895c0ca6ec5844ae7fd04))
* confine the claude worker to its worktree, and flag a write outside it ([#359](https://github.com/suzworx/flywheel/issues/359)) ([#370](https://github.com/suzworx/flywheel/issues/370)) ([210afbe](https://github.com/suzworx/flywheel/commit/210afbebe97c855a3996a30368f61fa8a661c541))
* detect a worker's plan when it is markdown-formatted or preceded by prose ([#288](https://github.com/suzworx/flywheel/issues/288)) ([cd0d8f3](https://github.com/suzworx/flywheel/commit/cd0d8f3bbeff26265f65425b4976a963371da599))
* enforce limits.per_host and the wave budget, which were parsed and never read ([#293](https://github.com/suzworx/flywheel/issues/293)) ([db181f3](https://github.com/suzworx/flywheel/commit/db181f3092fdd328e903889b088889905fb64bff))
* feedback reports the real untriaged signals ([#279](https://github.com/suzworx/flywheel/issues/279)) ([adba847](https://github.com/suzworx/flywheel/commit/adba8474ff0cbd202d2d877cb11e969aea247f1e))
* flywheel next waits a task whose owns overlap one in flight or one it already chose ([#326](https://github.com/suzworx/flywheel/issues/326)) ([a5e56a5](https://github.com/suzworx/flywheel/commit/a5e56a5b372d1cda207fb9a95ffbdcc57166be9a))
* git-write needs a worker write the guard saw, not just a moved HEAD ([#361](https://github.com/suzworx/flywheel/issues/361)) ([#373](https://github.com/suzworx/flywheel/issues/373)) ([7efd0eb](https://github.com/suzworx/flywheel/commit/7efd0eb9cfe4d3d79943618d8b951926ad0f0f1b))
* needs: none means no dependency, and needs: takes a comma list ([#310](https://github.com/suzworx/flywheel/issues/310)) ([e8c100c](https://github.com/suzworx/flywheel/commit/e8c100c3688bbdbe93f96e4274f03201a15ed89d))
* planned and amended keep the planner's identity and goal link; review records the reviewer's model ([#280](https://github.com/suzworx/flywheel/issues/280)) ([3e87c47](https://github.com/suzworx/flywheel/commit/3e87c47db0ce3370fec3eec98e7c650e5bd48992))
* record the claude adapter's token usage from the result line ([#290](https://github.com/suzworx/flywheel/issues/290)) ([35ebaa9](https://github.com/suzworx/flywheel/commit/35ebaa9f4a028197564bff2312be12cfc8b536ee))
* refuse an amendment that cannot change what is measured ([#274](https://github.com/suzworx/flywheel/issues/274)) ([dbaa1bd](https://github.com/suzworx/flywheel/commit/dbaa1bdca16866f8a09a0aefd5f70d2c09f8482c))
* supervise re-measures a failed unit once its owned files change ([#320](https://github.com/suzworx/flywheel/issues/320)) ([a5caab3](https://github.com/suzworx/flywheel/commit/a5caab30663dea4a4b481ee6ac0d6c171fc7ae3b))
* the owns check counts a unit's committed changes since its dispatch, not only uncommitted ones ([#338](https://github.com/suzworx/flywheel/issues/338)) ([8f326e5](https://github.com/suzworx/flywheel/commit/8f326e5ea60c1fb45d5a600c7b24e296e0abc15d))
* validate names owned files git will never commit ([#363](https://github.com/suzworx/flywheel/issues/363)) ([#374](https://github.com/suzworx/flywheel/issues/374)) ([b9bd964](https://github.com/suzworx/flywheel/commit/b9bd964130ce084ba948abf4e5b669e352860635))
* warn when a re-plan cannot change a dispatched attempt's gates; lint backticks in gates ([#366](https://github.com/suzworx/flywheel/issues/366)) ([#372](https://github.com/suzworx/flywheel/issues/372)) ([9384d01](https://github.com/suzworx/flywheel/commit/9384d01924ec257da5097d3a47ccb0ce427e3ac1))


### Documentation

* HUMAN.md walks a person through leading the agents from the terminal ([#337](https://github.com/suzworx/flywheel/issues/337)) ([66f6ff7](https://github.com/suzworx/flywheel/commit/66f6ff7812856ce72e9862c3f3a9b15ee65650c2))
* planner gates for units that write prose about the codebase ([#368](https://github.com/suzworx/flywheel/issues/368)) ([#369](https://github.com/suzworx/flywheel/issues/369)) ([d436840](https://github.com/suzworx/flywheel/commit/d43684038db3796939d18398629ab8b6c3648b0f))
* README describes the factory as it is — agent CLIs, audit, watch, measured unit cost ([#313](https://github.com/suzworx/flywheel/issues/313)) ([c8320ce](https://github.com/suzworx/flywheel/commit/c8320ceeb71f1ccf925b33435190c5177e4ae95c))

## [0.17.0](https://github.com/suzworx/flywheel/compare/v0.16.0...v0.17.0) (2026-09-17)


### Features

* record the planner on planned, and the tree on landed ([#269](https://github.com/suzworx/flywheel/issues/269)) ([56067de](https://github.com/suzworx/flywheel/commit/56067de477ac77b1824db9fbcedb65c306a42c70))
* warn at dispatch when in-flight units share a gate ([#268](https://github.com/suzworx/flywheel/issues/268)) ([54339df](https://github.com/suzworx/flywheel/commit/54339df1c9cbf147a5a580a8c61175f7debb3a3e))


### Bug Fixes

* lint says how to declare an owns: path the unit creates ([#266](https://github.com/suzworx/flywheel/issues/266)) ([d7834ec](https://github.com/suzworx/flywheel/commit/d7834ec642f2669ca22429bc4d6d7ee690a02a7f))

## [0.16.0](https://github.com/suzworx/flywheel/compare/v0.15.0...v0.16.0) (2026-09-17)


### Features

* serialise the feedback mutation, and add flywheel feedback regen ([#264](https://github.com/suzworx/flywheel/issues/264)) ([52d98af](https://github.com/suzworx/flywheel/commit/52d98af92d69ffb2a5060754f77db70227b772fc))


### Bug Fixes

* record the parsed brief header in the ledger ([#263](https://github.com/suzworx/flywheel/issues/263)) ([f8b6eb2](https://github.com/suzworx/flywheel/commit/f8b6eb211c1a035006c3a1a95bae6e489f193457))

## [0.15.0](https://github.com/suzworx/flywheel/compare/v0.14.0...v0.15.0) (2026-09-17)


### Features

* enforce exclusive: at dispatch — it was parsed and never read ([#232](https://github.com/suzworx/flywheel/issues/232)) ([3a8691b](https://github.com/suzworx/flywheel/commit/3a8691bdeb1c25e6438b222edd0499950be14f01)), closes [#220](https://github.com/suzworx/flywheel/issues/220)
* flywheel claim-edit, so a lead's mid-wave edit stops stranding units ([#256](https://github.com/suzworx/flywheel/issues/256)) ([cedcc14](https://github.com/suzworx/flywheel/commit/cedcc14899fceda16669952bb5aa849c759b982e))
* flywheel feedback export and submit, with consent and an offline outbox ([#226](https://github.com/suzworx/flywheel/issues/226)) ([afddfc6](https://github.com/suzworx/flywheel/commit/afddfc6ec4de367b35cd2a6ca8a4b86ba7714f28)), closes [#40](https://github.com/suzworx/flywheel/issues/40)
* inspect accepts a reading whose tree differs only outside the unit's owns ([#234](https://github.com/suzworx/flywheel/issues/234)) ([7beb1c3](https://github.com/suzworx/flywheel/commit/7beb1c327794569a167d8d5e3aeeccedbed27de7)), closes [#218](https://github.com/suzworx/flywheel/issues/218)
* record a signal event alongside the conditions already detected ([#224](https://github.com/suzworx/flywheel/issues/224)) ([7b52e4d](https://github.com/suzworx/flywheel/commit/7b52e4d1686f6113aa550cda30bfa974a1cb12c6)), closes [#37](https://github.com/suzworx/flywheel/issues/37)
* record the commit each gate reading was taken at ([#236](https://github.com/suzworx/flywheel/issues/236)) ([3625a17](https://github.com/suzworx/flywheel/commit/3625a17b9e882699a3cf6d921e8ef31f51369c91)), closes [#196](https://github.com/suzworx/flywheel/issues/196)


### Bug Fixes

* bind the learnings ownership check to the file it overwrites or deletes ([#245](https://github.com/suzworx/flywheel/issues/245)) ([6b109fa](https://github.com/suzworx/flywheel/commit/6b109faeb43a8770a038f14ce02151ee2648cf43))
* check learnings ownership at the rename, and never duplicate on a failed render ([#257](https://github.com/suzworx/flywheel/issues/257)) ([9e58f7c](https://github.com/suzworx/flywheel/commit/9e58f7c7b5980d52bc462ee40d724ab727ad602b))
* generate learnings.md inside .flywheel/, and remove the stray root copy ([#233](https://github.com/suzworx/flywheel/issues/233)) ([f656dc4](https://github.com/suzworx/flywheel/commit/f656dc478e62a1bed08910e81b5b0c25c2d2f159)), closes [#221](https://github.com/suzworx/flywheel/issues/221)
* judge each inspected pass against the gate set in force at the time ([#255](https://github.com/suzworx/flywheel/issues/255)) ([6a6e1f6](https://github.com/suzworx/flywheel/commit/6a6e1f6335ac79ee368685bd2a9fba5eeb2c7afa))
* never overwrite or delete a learnings.md flywheel did not generate ([#237](https://github.com/suzworx/flywheel/issues/237)) ([1767b57](https://github.com/suzworx/flywheel/commit/1767b5799699342ccd1b68982748a7e6a910d94a))
* record the tree inspect measured, not a second hash taken later ([#247](https://github.com/suzworx/flywheel/issues/247)) ([35c8e60](https://github.com/suzworx/flywheel/commit/35c8e6074a21ef6c53014fb1a790e453551fc5ab))
* resolve HEAD at each gauge reading, not once per validation pass ([#246](https://github.com/suzworx/flywheel/issues/246)) ([1b9c6b9](https://github.com/suzworx/flywheel/commit/1b9c6b942671f9fa2654964f4e257db115c59834))
* serialise dispatch, and carry exclusive: through correction deltas ([#248](https://github.com/suzworx/flywheel/issues/248)) ([dea7fe5](https://github.com/suzworx/flywheel/commit/dea7fe5331febfa51a683b2543585cced2d0a236))


### Documentation

* four planner rules from field reports, and one worker rule ([#235](https://github.com/suzworx/flywheel/issues/235)) ([8b98d2c](https://github.com/suzworx/flywheel/commit/8b98d2c5b9a1cd29d0d2376ba6f663004df402ea)), closes [#222](https://github.com/suzworx/flywheel/issues/222) [#229](https://github.com/suzworx/flywheel/issues/229) [#230](https://github.com/suzworx/flywheel/issues/230) [#231](https://github.com/suzworx/flywheel/issues/231)
* make the install steps actually install, and show the page without JS ([#250](https://github.com/suzworx/flywheel/issues/250)) ([38a3c43](https://github.com/suzworx/flywheel/commit/38a3c4327692d1600478f7a083ff3dae43d66852))
* planner and inspector rules for reachability — routed is not reachable ([#227](https://github.com/suzworx/flywheel/issues/227)) ([0b549eb](https://github.com/suzworx/flywheel/commit/0b549eb5a3df397911d357bb2893c236824e0e41)), closes [#197](https://github.com/suzworx/flywheel/issues/197)
* redesign the landing page, and finally show the tool ([#238](https://github.com/suzworx/flywheel/issues/238)) ([2457ea2](https://github.com/suzworx/flywheel/commit/2457ea24bea9774063e9a3d6ab0c4a87666852e1))

## [0.14.0](https://github.com/suzworx/flywheel/compare/v0.13.0...v0.14.0) (2026-09-17)


### Features

* record a lead-implemented unit as an event, not as a story ([#216](https://github.com/suzworx/flywheel/issues/216)) ([cc913b8](https://github.com/suzworx/flywheel/commit/cc913b8e22dc03d9eadf08c5a211729a86dfca53)), closes [#198](https://github.com/suzworx/flywheel/issues/198)
* warn at dispatch when a brief has drifted from the hash last dispatched ([#215](https://github.com/suzworx/flywheel/issues/215)) ([469009e](https://github.com/suzworx/flywheel/commit/469009e260eddf1c4a584ecbe91a48af6289ebde)), closes [#135](https://github.com/suzworx/flywheel/issues/135)


### Bug Fixes

* give a claude worker the toolchain, without giving it git writes ([#214](https://github.com/suzworx/flywheel/issues/214)) ([e8af57f](https://github.com/suzworx/flywheel/commit/e8af57fec3b276932f3739fa623ed576b1198e64)), closes [#192](https://github.com/suzworx/flywheel/issues/192)


### Documentation

* link the quickstart from the site, which had no way into it ([#212](https://github.com/suzworx/flywheel/issues/212)) ([b6e38a1](https://github.com/suzworx/flywheel/commit/b6e38a10d28114fa3678c64cf0493281a1d76277))

## [0.13.0](https://github.com/suzworx/flywheel/compare/v0.12.0...v0.13.0) (2026-09-16)


### Features

* attribute another worktree's own in-flight work instead of blaming this unit ([#209](https://github.com/suzworx/flywheel/issues/209)) ([7beea89](https://github.com/suzworx/flywheel/commit/7beea89292dedf73ee112b54a3757866b12f156c)), closes [#200](https://github.com/suzworx/flywheel/issues/200)


### Documentation

* add a quickstart and a concepts page ([#211](https://github.com/suzworx/flywheel/issues/211)) ([b199862](https://github.com/suzworx/flywheel/commit/b199862a426a458026aa20f3cd82bb470b31cb5a))

## [0.12.0](https://github.com/suzworx/flywheel/compare/v0.11.0...v0.12.0) (2026-09-16)


### Features

* add flywheel upgrade, a self-update with checksum verification ([#205](https://github.com/suzworx/flywheel/issues/205)) ([937ed95](https://github.com/suzworx/flywheel/commit/937ed95fc61ab8c5f4d802737f725d498d9fcf75)), closes [#201](https://github.com/suzworx/flywheel/issues/201)
* record each changed file's shape in the readings, so a truncated document is visible ([#206](https://github.com/suzworx/flywheel/issues/206)) ([58ba50d](https://github.com/suzworx/flywheel/commit/58ba50d58073f042dfedeafedb7eecad03d04048))
* refuse an owns collision with a running unit at dispatch ([#204](https://github.com/suzworx/flywheel/issues/204)) ([454c11c](https://github.com/suzworx/flywheel/commit/454c11c3d66fee246f001a1f5e0a90cc9e17adf9)), closes [#164](https://github.com/suzworx/flywheel/issues/164)


### Documentation

* add the flywheel mark, and a GitHub Pages landing page ([#202](https://github.com/suzworx/flywheel/issues/202)) ([16798e1](https://github.com/suzworx/flywheel/commit/16798e139eea0a804bdc989c9ab984d840271137))
* refresh the terminal screenshots and link the docs site ([#207](https://github.com/suzworx/flywheel/issues/207)) ([c432b18](https://github.com/suzworx/flywheel/commit/c432b18168e76b8f280cf8f77dff61c73f21688b))
* say what is deterministic, what is not, and what that guarantees ([#208](https://github.com/suzworx/flywheel/issues/208)) ([ec61b2b](https://github.com/suzworx/flywheel/commit/ec61b2be334dbafa2d7cd296ce4dd505475713d5))

## [0.11.0](https://github.com/suzworx/flywheel/compare/v0.10.0...v0.11.0) (2026-09-16)


### Features

* add live-gate:, a gate that runs only in the lead's verification pass ([#191](https://github.com/suzworx/flywheel/issues/191)) ([4861685](https://github.com/suzworx/flywheel/commit/4861685)), closes [#152](https://github.com/suzworx/flywheel/issues/152)
* add flywheel doctor, and gate a resumed model switch on approved fallbacks ([#190](https://github.com/suzworx/flywheel/issues/190)) ([e2794ea](https://github.com/suzworx/flywheel/commit/e2794eaad0b379100171370529cef01a82c6811a)), closes [#23](https://github.com/suzworx/flywheel/issues/23)
* add flywheel feedback: add, list, dismiss, and a generated learnings.md ([#195](https://github.com/suzworx/flywheel/issues/195)) ([dd7e1cd](https://github.com/suzworx/flywheel/commit/dd7e1cd54713c58247a7168a1164e7e2d62c6704))
* record a reviewer's domain checklist on the reviewed event ([#184](https://github.com/suzworx/flywheel/issues/184)) ([c394358](https://github.com/suzworx/flywheel/commit/c3943580fe88e64d88f0cecd6df00e35f0c32250)), closes [#32](https://github.com/suzworx/flywheel/issues/32)


### Bug Fixes

* a failed claude run is not a clean stop, and --resume now resumes ([#194](https://github.com/suzworx/flywheel/issues/194)) ([d88b9f9](https://github.com/suzworx/flywheel/commit/d88b9f9bfd6bbb932aa72007807664bfc0e48d9a)), closes [#188](https://github.com/suzworx/flywheel/issues/188) [#189](https://github.com/suzworx/flywheel/issues/189)
* count a claude worker's model turns, so the andon works on that adapter ([#199](https://github.com/suzworx/flywheel/issues/199)) ([05b8dcd](https://github.com/suzworx/flywheel/commit/05b8dcd6ad26f8ee5c6236972a40106edfba8ea9)), closes [#187](https://github.com/suzworx/flywheel/issues/187)
* stop blaming another worktree's generated flywheel.md on a unit ([#193](https://github.com/suzworx/flywheel/issues/193)) ([fec4e50](https://github.com/suzworx/flywheel/commit/fec4e50ff79e1bd1078fcb42aa08d4764f853ecf)), closes [#186](https://github.com/suzworx/flywheel/issues/186)

## [0.10.0](https://github.com/suzworx/flywheel/compare/v0.9.0...v0.10.0) (2026-09-16)


### Features

* carry declared machine state into an isolated workdir ([#182](https://github.com/suzworx/flywheel/issues/182)) ([b38acc8](https://github.com/suzworx/flywheel/commit/b38acc809d550d4607308f74cffe80842a948782))
* record the files an attempt wrote, and name them when it fails ([#180](https://github.com/suzworx/flywheel/issues/180)) ([9fa4533](https://github.com/suzworx/flywheel/commit/9fa453353314aaed252c913ddda4779db9f1616b))


### Documentation

* say exactly what is verified about the claude adapter ([#183](https://github.com/suzworx/flywheel/issues/183)) ([27d61b8](https://github.com/suzworx/flywheel/commit/27d61b861781634e30ac809a04822a10a68fd5c7))

## [0.9.0](https://github.com/suzworx/flywheel/compare/v0.8.0...v0.9.0) (2026-09-16)


### Features

* a Claude worker adapter, and dispatch for every subprocess adapter ([#168](https://github.com/suzworx/flywheel/issues/168)) ([cf2bee2](https://github.com/suzworx/flywheel/commit/cf2bee2bd22835618065a3b1698bffcb003a9df0))
* attribute a changed path to the in-flight unit that owns it ([#173](https://github.com/suzworx/flywheel/issues/173)) ([ee9befd](https://github.com/suzworx/flywheel/commit/ee9befd6080966451a3dc9d4f7691562e7b78895))
* flywheel claim, release and claims — two leads on one repo ([#171](https://github.com/suzworx/flywheel/issues/171)) ([40cc015](https://github.com/suzworx/flywheel/commit/40cc015cedcae35a7e61550eabc300b5ad83f93c))
* flywheel review runs a unit's gates and owns check in an isolated worktree ([#178](https://github.com/suzworx/flywheel/issues/178)) ([0e150d9](https://github.com/suzworx/flywheel/commit/0e150d98bfc6c3d253d91a55eb25de66fdcea03b))
* load the worker rules on every run ([#174](https://github.com/suzworx/flywheel/issues/174)) ([6e2b423](https://github.com/suzworx/flywheel/commit/6e2b423b094c84cd5474120cd8d44ba73da60dab))
* planned and amended events record the brief's owns and needs ([#175](https://github.com/suzworx/flywheel/issues/175)) ([b1986f9](https://github.com/suzworx/flywheel/commit/b1986f9a5fbb1da8068bf0c7d3922e7286dd6639))
* raise the andon for a run that reads without writing and states no plan ([#179](https://github.com/suzworx/flywheel/issues/179)) ([72822b1](https://github.com/suzworx/flywheel/commit/72822b159677bd14d07477aec724a56b01fd90e9))
* report a gate failure caused entirely outside owns as inconclusive ([#166](https://github.com/suzworx/flywheel/issues/166)) ([a928a64](https://github.com/suzworx/flywheel/commit/a928a6441d2e76facee4d4d2f2e349e62eb400ec))


### Bug Fixes

* a fresh dispatch is never a correction, whatever the prompt path says ([#170](https://github.com/suzworx/flywheel/issues/170)) ([45539bf](https://github.com/suzworx/flywheel/commit/45539bfc2ae3e9190578bc5fef79d22dfb0e18e1))


### Documentation

* a swallowed error on an external call is a finding ([#177](https://github.com/suzworx/flywheel/issues/177)) ([383a380](https://github.com/suzworx/flywheel/commit/383a380ae8c3a947ad8aec0b0b66784f617c22d6))
* bring the README, the skills and the demo script up to date ([#172](https://github.com/suzworx/flywheel/issues/172)) ([c2c6f6d](https://github.com/suzworx/flywheel/commit/c2c6f6dcb0db0c682b89977959015a5b49f96054))

## [0.8.0](https://github.com/suzworx/flywheel/compare/v0.7.0...v0.8.0) (2026-09-16)


### Features

* a silent finish says why, and every finish names the model ([#149](https://github.com/suzworx/flywheel/issues/149)) ([718d663](https://github.com/suzworx/flywheel/commit/718d663a9015ec3c84bae4835615f10e733eab12))
* add flywheel stats, the factory's own numbers ([#146](https://github.com/suzworx/flywheel/issues/146)) ([f6267fb](https://github.com/suzworx/flywheel/commit/f6267fb31c6ef14193c6cb2fadde18d981f15d8d))
* capture agent sessions and add flywheel trace ([#157](https://github.com/suzworx/flywheel/issues/157)) ([86bd162](https://github.com/suzworx/flywheel/commit/86bd1628b13e10f3b821ebc31d56aefb33281e28))
* confine a worker to its worktree ([#161](https://github.com/suzworx/flywheel/issues/161)) ([7cbeae8](https://github.com/suzworx/flywheel/commit/7cbeae8af8fa654ce0d3060fee177c067352332c))
* flag a run that reaches step 20 with no plan check-in ([#150](https://github.com/suzworx/flywheel/issues/150)) ([89a5c3d](https://github.com/suzworx/flywheel/commit/89a5c3d6a129c16488388f865bfb27bacdc4fb61))
* flywheel init --agents-md, and vendor-neutral lead references ([#151](https://github.com/suzworx/flywheel/issues/151)) ([ddc86bc](https://github.com/suzworx/flywheel/commit/ddc86bc6ded90d0a68c90e1c98e1817445644057))
* init --track/--ignore decides whether flywheel.md is committed ([#148](https://github.com/suzworx/flywheel/issues/148)) ([2f11c45](https://github.com/suzworx/flywheel/commit/2f11c4559b3bea17a261bbf57b0f7cdeaf985a7b))
* record an off-course signal when a run reads outside its worktree ([#154](https://github.com/suzworx/flywheel/issues/154)) ([b94e330](https://github.com/suzworx/flywheel/commit/b94e33035bec4e4739f3325a5b43c75a2227dc98))
* record the peak single-step reasoning, and hint when a run is capped ([#156](https://github.com/suzworx/flywheel/issues/156)) ([b21a79e](https://github.com/suzworx/flywheel/commit/b21a79ea112e871f7e96fc79f80d2e4ca5e507e0))
* stamp the release version into every skill, and warn on stale skills ([#145](https://github.com/suzworx/flywheel/issues/145)) ([5a05139](https://github.com/suzworx/flywheel/commit/5a051394a2d9e9bdef013ba24d47681bb0e51019))
* stop and record a run that goes silent mid-stream ([#158](https://github.com/suzworx/flywheel/issues/158)) ([bacf3cf](https://github.com/suzworx/flywheel/commit/bacf3cfaa8f6acbf58a3683d12ca61179b134bbf))


### Bug Fixes

* a run cut off by the output cap no longer reads as done ([#143](https://github.com/suzworx/flywheel/issues/143)) ([b061afd](https://github.com/suzworx/flywheel/commit/b061afdd91dbab0dd27e655db7bacc0763af2c07))
* goal help shows its arguments, and log --goal refuses an unknown goal ([#140](https://github.com/suzworx/flywheel/issues/140)) ([8891722](https://github.com/suzworx/flywheel/commit/8891722e6e8dcb1673adbef8e1c8d314c0931f44))
* init fills in missing scaffold pieces instead of refusing ([#142](https://github.com/suzworx/flywheel/issues/142)) ([e1601eb](https://github.com/suzworx/flywheel/commit/e1601ebcc3e1ce3ab9fdb5eeab3a98e24b38472a))
* validate tells a persistent host block from a flaky one ([#147](https://github.com/suzworx/flywheel/issues/147)) ([dfceda1](https://github.com/suzworx/flywheel/commit/dfceda14bba88df514d835a40e29fab265532835))
* validate, inspect and verify measure the attempt's own prompt ([#144](https://github.com/suzworx/flywheel/issues/144)) ([ca5a11f](https://github.com/suzworx/flywheel/commit/ca5a11f5f3e6fb2c89ad228612cba68e0df1911c))


### Documentation

* bring the protocol up to date with the kinds and codes that followed it ([#159](https://github.com/suzworx/flywheel/issues/159)) ([b017c08](https://github.com/suzworx/flywheel/commit/b017c08152257e2e2b4465ae6c8653f5677f4ac1))
* write the flywheel protocol and cite it from every skill ([#155](https://github.com/suzworx/flywheel/issues/155)) ([c7203f6](https://github.com/suzworx/flywheel/commit/c7203f6d56faf1c4c5337172f188f025c4ab84c7))

## [0.7.0](https://github.com/suzworx/flywheel/compare/v0.6.0...v0.7.0) (2026-09-16)


### Features

* lint accepts owns patterns, and the docs teach the glob form ([#139](https://github.com/suzworx/flywheel/issues/139)) ([9ba42d5](https://github.com/suzworx/flywheel/commit/9ba42d5d67a73c52521ba22281d872ece6450987))


### Documentation

* document --workdir for validating while other units run ([#137](https://github.com/suzworx/flywheel/issues/137)) ([8900aa4](https://github.com/suzworx/flywheel/commit/8900aa4ae24c45047d9f19521f29c60f730cdf7f))

## [0.6.0](https://github.com/suzworx/flywheel/compare/v0.5.0...v0.6.0) (2026-09-15)


### Features

* a pure Reconcile and a read-only flywheel next ([#118](https://github.com/suzworx/flywheel/issues/118)) ([550c016](https://github.com/suzworx/flywheel/commit/550c016671ce7b5b5035f0ac05db1cf09ffcd477)), closes [#27](https://github.com/suzworx/flywheel/issues/27)
* flywheel cost sums tokens and cost per task and per model ([#122](https://github.com/suzworx/flywheel/issues/122)) ([70c5b6f](https://github.com/suzworx/flywheel/commit/70c5b6f72c1faf5074ddc66c84aab34a311a8245)), closes [#29](https://github.com/suzworx/flywheel/issues/29)
* flywheel handoff summarizes the factory for a new head ([#126](https://github.com/suzworx/flywheel/issues/126)) ([4eca0cc](https://github.com/suzworx/flywheel/commit/4eca0cc2ab10aa508a92aca1ff6a295684ee199d)), closes [#30](https://github.com/suzworx/flywheel/issues/30)
* flywheel lint checks a brief before dispatch ([#124](https://github.com/suzworx/flywheel/issues/124)) ([64a8a3d](https://github.com/suzworx/flywheel/commit/64a8a3d2d69f993c533ea992dac5a087789128fc)), closes [#28](https://github.com/suzworx/flywheel/issues/28)
* init and config validate report what they did; log help lists both verdict sets ([#128](https://github.com/suzworx/flywheel/issues/128)) ([d7b9b29](https://github.com/suzworx/flywheel/commit/d7b9b29d340535f367615914d304134d89ddde79))
* the controller loop records lost and blocked work under a single-controller lock ([#129](https://github.com/suzworx/flywheel/issues/129)) ([0217e2e](https://github.com/suzworx/flywheel/commit/0217e2e6e2dcb2f486af99986d1a7d8a3e622d42))


### Bug Fixes

* run prints the cost rounded to four decimals ([#123](https://github.com/suzworx/flywheel/issues/123)) ([f29487a](https://github.com/suzworx/flywheel/commit/f29487a597ac93db3d8170de36c289ddeed92089)), closes [#81](https://github.com/suzworx/flywheel/issues/81)
* status durations in human units; upgrade notes ([#125](https://github.com/suzworx/flywheel/issues/125)) ([0acb09a](https://github.com/suzworx/flywheel/commit/0acb09a1f967f52418ba94bbf6b8da02c2d29d31))
* the factory's STAGE column shows passed and rejected ([#119](https://github.com/suzworx/flywheel/issues/119)) ([42e204f](https://github.com/suzworx/flywheel/commit/42e204fc9f5de614b30718ccd69b813eb03a2a6a)), closes [#110](https://github.com/suzworx/flywheel/issues/110)

## [0.5.0](https://github.com/suzworx/flywheel/compare/v0.4.0...v0.5.0) (2026-09-15)


### Features

* deterministic runtime phases 1-3: stale results, status, goals, leases ([#112](https://github.com/suzworx/flywheel/issues/112)) ([1f975a2](https://github.com/suzworx/flywheel/commit/1f975a2efd868777abec72dc6568c743c4c1af1a))
* factory ages in human units; flywheel land records a merge ([#114](https://github.com/suzworx/flywheel/issues/114)) ([2941fc5](https://github.com/suzworx/flywheel/commit/2941fc5960666f40391c53ed35e329665a7e5b10)), closes [#110](https://github.com/suzworx/flywheel/issues/110)


### Bug Fixes

* brief hashes survive a checkout that converts line endings ([#115](https://github.com/suzworx/flywheel/issues/115)) ([adccd18](https://github.com/suzworx/flywheel/commit/adccd187671c57615f7cb825e2a22ee27f1e43d3)), closes [#111](https://github.com/suzworx/flywheel/issues/111)

## [0.4.0](https://github.com/suzworx/flywheel/compare/v0.3.0...v0.4.0) (2026-09-15)


### Features

* flywheel help, config set, staff, and stricter gauges ([#107](https://github.com/suzworx/flywheel/issues/107)) ([fbd8d67](https://github.com/suzworx/flywheel/commit/fbd8d67ea3090b5831522559a910b074c097cb04))


### Bug Fixes

* run --delta without --resume sends the delta ([#108](https://github.com/suzworx/flywheel/issues/108)) ([f51cd99](https://github.com/suzworx/flywheel/commit/f51cd996dde4172fff17b965d7635eb59f2d055c)), closes [#106](https://github.com/suzworx/flywheel/issues/106)


### Documentation

* add AGENTS.md, the build manual for agents working on flywheel ([#99](https://github.com/suzworx/flywheel/issues/99)) ([23d712c](https://github.com/suzworx/flywheel/commit/23d712cac0d2bbddde5b269519dc0054787593ae))
* deterministic factory runtime, Phase 0 (report, gaps, design, plan) ([#109](https://github.com/suzworx/flywheel/issues/109)) ([fa2ba45](https://github.com/suzworx/flywheel/commit/fa2ba45600f4ecb1956e742245829e7db3add909))
* flywheel is built by flywheel; the repo moves to suzworx ([#104](https://github.com/suzworx/flywheel/issues/104)) ([6b5df3d](https://github.com/suzworx/flywheel/commit/6b5df3d048748bb63d569ca3fa9778e6641ed45c))
* the factory is files — owned setup, run intelligence ([#103](https://github.com/suzworx/flywheel/issues/103)) ([57c7f3a](https://github.com/suzworx/flywheel/commit/57c7f3abe755c3b33a2ead039cb308b4e0dd9ada))

## [0.3.0](https://github.com/go2sujeet/flywheel/compare/v0.2.0...v0.3.0) (2026-09-13)


### Features

* add flywheel factory, the live view of the factory floor ([#88](https://github.com/go2sujeet/flywheel/issues/88)) ([5d0a5f7](https://github.com/go2sujeet/flywheel/commit/5d0a5f7192e3865ba34612a63fd8c3cba2d577dd))
* add flywheel run, one correct OpenCode dispatch recorded as events ([#78](https://github.com/go2sujeet/flywheel/issues/78)) ([d8e8faa](https://github.com/go2sujeet/flywheel/commit/d8e8faaebef8bcdc1d48f08f601b1ac7087f639c))
* add machine gauges, inspection and verify (validate, inspect, verify) ([#86](https://github.com/go2sujeet/flywheel/issues/86)) ([b86a562](https://github.com/go2sujeet/flywheel/commit/b86a5628d8f07b03473baf92525a2de885071230))


### Documentation

* fold dogfooding learnings into the skills ([#77](https://github.com/go2sujeet/flywheel/issues/77)) ([6c1ce29](https://github.com/go2sujeet/flywheel/commit/6c1ce2902db5399ff96728e73e1dc458998a9b35))
* lead the README with the factory, add real CLI screenshots ([#75](https://github.com/go2sujeet/flywheel/issues/75)) ([77de297](https://github.com/go2sujeet/flywheel/commit/77de2972809cb954971d5765155b3eeac29b8301))
* README screenshots for the gauges and the factory floor ([#90](https://github.com/go2sujeet/flywheel/issues/90)) ([664325d](https://github.com/go2sujeet/flywheel/commit/664325db78dc180b89d135a02fa84188f7c4b216))
* teach the skills the gauges, the factory view and measured worker tactics ([#89](https://github.com/go2sujeet/flywheel/issues/89)) ([b43c293](https://github.com/go2sujeet/flywheel/commit/b43c293655c8851e69faba001a1f132766fcea18))

## [0.2.0](https://github.com/go2sujeet/flywheel/compare/v0.1.2...v0.2.0) (2026-09-13)


### Features

* add an append-only event log with flywheel log and flywheel state ([#70](https://github.com/go2sujeet/flywheel/issues/70)) ([6ca2ba0](https://github.com/go2sujeet/flywheel/commit/6ca2ba082ed9130e520a2525a6e6a25e5d5007ca))
* add the project config package for .flywheel/config.json ([#67](https://github.com/go2sujeet/flywheel/issues/67)) ([f7c1a70](https://github.com/go2sujeet/flywheel/commit/f7c1a70c5bf360ff6fa85105d06808f94bbcc3f4))


### Documentation

* add factory persona skills ([#66](https://github.com/go2sujeet/flywheel/issues/66)) ([d379f14](https://github.com/go2sujeet/flywheel/commit/d379f1460fede4742ca2839ca309b60b8cc39677))
* autonomous shipping protocol and the flywheel factory model ([#64](https://github.com/go2sujeet/flywheel/issues/64)) ([4c84060](https://github.com/go2sujeet/flywheel/commit/4c8406083f536a7e8eeb120d02a51532d850f473))
* design for native feedback, personas, scale and offline ([#34](https://github.com/go2sujeet/flywheel/issues/34)) ([48ac99c](https://github.com/go2sujeet/flywheel/commit/48ac99cd13d0c55a7671a7aac68be270204dc4f7))
* fold consumer feedback into the skills ([#33](https://github.com/go2sujeet/flywheel/issues/33)) ([6db2bb1](https://github.com/go2sujeet/flywheel/commit/6db2bb19bbac20c62d5dcb62338c3c505c59d396))
* forbid workers from rewriting the shared tree and ship the deny policy ([#71](https://github.com/go2sujeet/flywheel/issues/71)) ([561a85f](https://github.com/go2sujeet/flywheel/commit/561a85f882534f77c48722613d71230df29c53ee))

## [0.1.2](https://github.com/go2sujeet/flywheel/compare/v0.1.1...v0.1.2) (2026-09-12)

### Bug Fixes

* release builds stamp the version into `flywheel version` ([#8](https://github.com/go2sujeet/flywheel/pull/8))

### Documentation

* fold field-run findings into the flywheel skills ([#7](https://github.com/go2sujeet/flywheel/pull/7))

## [0.1.1](https://github.com/go2sujeet/flywheel/compare/v0.1.0...v0.1.1) (2026-09-12)

### Bug Fixes

* harden flywheel init and correct skill contracts ([#6](https://github.com/go2sujeet/flywheel/pull/6))

## 0.1.0 (2026-09-10)

### Features

* Go CLI with `flywheel init` and `flywheel version`, plus the flywheel, flywheel-worker and
  flywheel-operator skills ([#5](https://github.com/go2sujeet/flywheel/pull/5))
