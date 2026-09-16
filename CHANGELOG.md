# Changelog

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
