# Changelog

## [0.11.0](https://github.com/suzworx/flywheel/compare/v0.10.0...v0.11.0) (2026-09-16)


### Features

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
