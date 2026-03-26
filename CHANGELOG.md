# Changelog

## [1.6.0](https://github.com/seznam/jailoc/compare/v1.5.0...v1.6.0) (2026-03-26)


### Features

* attach watchdog with container restart detection ([dbab710](https://github.com/seznam/jailoc/commit/dbab7102ed3923f0e23fdf6d667d6ff857035e28))
* cobra subcommands — up, down, attach, status, config, logs, add ([f3d5301](https://github.com/seznam/jailoc/commit/f3d530154b10d931aa9902e323e54751f1de62a0))
* defaults section, env/env_file support, and allowed-files write-ahead ([6207da8](https://github.com/seznam/jailoc/commit/6207da8d8caa991f4814d15741e8affdef871445))
* dockerfile URL presets with HTTP fetch, content-hash tagging, and validation ([3da02ce](https://github.com/seznam/jailoc/commit/3da02ceb55105db7b16ca7262b7c98471e7eb705))
* entrypoint firewall rules, integration tests, README rewrite, and cleanup ([c8b3346](https://github.com/seznam/jailoc/commit/c8b334627ce99659427e3cb02c851009c124dc00))
* Go CLI core — embedded assets, config, workspace, compose, docker packages ([8b6e3a8](https://github.com/seznam/jailoc/commit/8b6e3a8d41dbb227458225034787955d97bb58b1))
* initial sandbox with Dockerfile, DinD sidecar, network isolation, and firewall ([83eb4be](https://github.com/seznam/jailoc/commit/83eb4bed5cb39222a20f1ad64760d3f9d3b699f5))
* mount path validation and access modes (remote/exec) with CLI flags ([979ade2](https://github.com/seznam/jailoc/commit/979ade2815ef82b9b0872c4fa939065f5c69101a))
* progress messages and compose event processor ([f6b3b4f](https://github.com/seznam/jailoc/commit/f6b3b4f60db0ec66500353964f92c73e362bce62))
* up command with 4-tier image resolution, CWD detection, and GoReleaser ([ab251c7](https://github.com/seznam/jailoc/commit/ab251c71cb1c93b1912d70573f15028763db1800))
* working_dir, mermaid diagrams, access modes page, and jailOC rebrand ([25bcfe8](https://github.com/seznam/jailoc/commit/25bcfe89895d60c00f9465df7685d09678928fe5))
* workspace image field, [image]→[base] rename, and mutual exclusivity ([4952608](https://github.com/seznam/jailoc/commit/495260875b012b0aab7381bceb05f35299d0b1ae))


### Bug Fixes

* add MIT license and contributing guide ([f7390f0](https://github.com/seznam/jailoc/commit/f7390f0b65f811cd036bdf1483aa7f319b87944e))
* **ci:** hide chore commits from release-please changelog ([#10](https://github.com/seznam/jailoc/issues/10)) ([267408a](https://github.com/seznam/jailoc/commit/267408a3b0f68f93de80dfb22c9c7cb0d0313f0c))
* **ci:** order release jobs — docs before binaries, draft until publish ([#7](https://github.com/seznam/jailoc/issues/7)) ([ba05ec9](https://github.com/seznam/jailoc/commit/ba05ec975fbb950c37dc20e29e8db94bd8b8d9b4))
* **ci:** use commit SHA for checkout in release workflow ([#13](https://github.com/seznam/jailoc/issues/13)) ([f21d9f1](https://github.com/seznam/jailoc/commit/f21d9f1a56ce4263d1cd1becf953f53ef062537f))
* DAC_READ_SEARCH cap, Diátaxis docs restructure, exec mode, and path argument ([78346bc](https://github.com/seznam/jailoc/commit/78346bcc3bc3688b5cea77d9b442ad2e6697fa2c))
* DinD networking, lint issues, progress display, readiness poll, and CVE patches ([cef4531](https://github.com/seznam/jailoc/commit/cef4531e91fe0149849798684df5cf54ee78639e))


### Code Refactoring

* rename module to github.com/seznam/jailoc and migrate to Docker Go SDKs ([a0909b8](https://github.com/seznam/jailoc/commit/a0909b833634b60029b2025992b95e2c21d5c697))
* two-tier base+overlay image resolution replacing cascade model ([377183a](https://github.com/seznam/jailoc/commit/377183a6407002dadd382632a6a3b71dca0097b5))


### Documentation

* Czech translation, character easter eggs, and content polish ([1fbe0b9](https://github.com/seznam/jailoc/commit/1fbe0b907d0fe71ff7722e3ddfb4bfb00dfe9870))
* landing page, branding, threat model, and Mermaid diagram polish ([4801ae0](https://github.com/seznam/jailoc/commit/4801ae0711f95f6270dfd87277e61684f9ed1f99))
* **readme:** add installation section ([#5](https://github.com/seznam/jailoc/issues/5)) ([db48ff6](https://github.com/seznam/jailoc/commit/db48ff63b53d0b9105c4eed4dbd493f3759b511b))
* scaffold MkDocs site with pages, dark theme, and downloads ([c700cf5](https://github.com/seznam/jailoc/commit/c700cf56754b0cc3b5185a45e3055034b5cfe766))


### Tests

* unit test coverage for cmd, compose, config, workspace, and docker ([d7ea5a8](https://github.com/seznam/jailoc/commit/d7ea5a82844763ebece8d198520d7da9ecc16a1a))

## [1.5.0](https://github.com/seznam/jailoc/compare/v1.4.2...v1.5.0) (2026-03-26)


### Features

* attach watchdog with container restart detection ([dbab710](https://github.com/seznam/jailoc/commit/dbab7102ed3923f0e23fdf6d667d6ff857035e28))
* cobra subcommands — up, down, attach, status, config, logs, add ([f3d5301](https://github.com/seznam/jailoc/commit/f3d530154b10d931aa9902e323e54751f1de62a0))
* defaults section, env/env_file support, and allowed-files write-ahead ([6207da8](https://github.com/seznam/jailoc/commit/6207da8d8caa991f4814d15741e8affdef871445))
* dockerfile URL presets with HTTP fetch, content-hash tagging, and validation ([3da02ce](https://github.com/seznam/jailoc/commit/3da02ceb55105db7b16ca7262b7c98471e7eb705))
* entrypoint firewall rules, integration tests, README rewrite, and cleanup ([c8b3346](https://github.com/seznam/jailoc/commit/c8b334627ce99659427e3cb02c851009c124dc00))
* Go CLI core — embedded assets, config, workspace, compose, docker packages ([8b6e3a8](https://github.com/seznam/jailoc/commit/8b6e3a8d41dbb227458225034787955d97bb58b1))
* initial sandbox with Dockerfile, DinD sidecar, network isolation, and firewall ([83eb4be](https://github.com/seznam/jailoc/commit/83eb4bed5cb39222a20f1ad64760d3f9d3b699f5))
* mount path validation and access modes (remote/exec) with CLI flags ([979ade2](https://github.com/seznam/jailoc/commit/979ade2815ef82b9b0872c4fa939065f5c69101a))
* progress messages and compose event processor ([f6b3b4f](https://github.com/seznam/jailoc/commit/f6b3b4f60db0ec66500353964f92c73e362bce62))
* up command with 4-tier image resolution, CWD detection, and GoReleaser ([ab251c7](https://github.com/seznam/jailoc/commit/ab251c71cb1c93b1912d70573f15028763db1800))
* working_dir, mermaid diagrams, access modes page, and jailOC rebrand ([25bcfe8](https://github.com/seznam/jailoc/commit/25bcfe89895d60c00f9465df7685d09678928fe5))
* workspace image field, [image]→[base] rename, and mutual exclusivity ([4952608](https://github.com/seznam/jailoc/commit/495260875b012b0aab7381bceb05f35299d0b1ae))


### Bug Fixes

* add MIT license and contributing guide ([f7390f0](https://github.com/seznam/jailoc/commit/f7390f0b65f811cd036bdf1483aa7f319b87944e))
* **ci:** hide chore commits from release-please changelog ([#10](https://github.com/seznam/jailoc/issues/10)) ([267408a](https://github.com/seznam/jailoc/commit/267408a3b0f68f93de80dfb22c9c7cb0d0313f0c))
* **ci:** order release jobs — docs before binaries, draft until publish ([#7](https://github.com/seznam/jailoc/issues/7)) ([ba05ec9](https://github.com/seznam/jailoc/commit/ba05ec975fbb950c37dc20e29e8db94bd8b8d9b4))
* DAC_READ_SEARCH cap, Diátaxis docs restructure, exec mode, and path argument ([78346bc](https://github.com/seznam/jailoc/commit/78346bcc3bc3688b5cea77d9b442ad2e6697fa2c))
* DinD networking, lint issues, progress display, readiness poll, and CVE patches ([cef4531](https://github.com/seznam/jailoc/commit/cef4531e91fe0149849798684df5cf54ee78639e))


### Code Refactoring

* rename module to github.com/seznam/jailoc and migrate to Docker Go SDKs ([a0909b8](https://github.com/seznam/jailoc/commit/a0909b833634b60029b2025992b95e2c21d5c697))
* two-tier base+overlay image resolution replacing cascade model ([377183a](https://github.com/seznam/jailoc/commit/377183a6407002dadd382632a6a3b71dca0097b5))


### Documentation

* Czech translation, character easter eggs, and content polish ([1fbe0b9](https://github.com/seznam/jailoc/commit/1fbe0b907d0fe71ff7722e3ddfb4bfb00dfe9870))
* landing page, branding, threat model, and Mermaid diagram polish ([4801ae0](https://github.com/seznam/jailoc/commit/4801ae0711f95f6270dfd87277e61684f9ed1f99))
* **readme:** add installation section ([#5](https://github.com/seznam/jailoc/issues/5)) ([db48ff6](https://github.com/seznam/jailoc/commit/db48ff63b53d0b9105c4eed4dbd493f3759b511b))
* scaffold MkDocs site with pages, dark theme, and downloads ([c700cf5](https://github.com/seznam/jailoc/commit/c700cf56754b0cc3b5185a45e3055034b5cfe766))


### Tests

* unit test coverage for cmd, compose, config, workspace, and docker ([d7ea5a8](https://github.com/seznam/jailoc/commit/d7ea5a82844763ebece8d198520d7da9ecc16a1a))

## [1.4.2](https://github.com/seznam/jailoc/compare/v1.4.1...v1.4.2) (2026-03-26)


### Bug Fixes

* **ci:** order release jobs — docs before binaries, draft until publish ([#7](https://github.com/seznam/jailoc/issues/7)) ([ba05ec9](https://github.com/seznam/jailoc/commit/ba05ec975fbb950c37dc20e29e8db94bd8b8d9b4))

## [1.4.1](https://github.com/seznam/jailoc/compare/v1.4.0...v1.4.1) (2026-03-26)


### Documentation

* **readme:** add installation section ([#5](https://github.com/seznam/jailoc/issues/5)) ([db48ff6](https://github.com/seznam/jailoc/commit/db48ff63b53d0b9105c4eed4dbd493f3759b511b))

## [1.4.0](https://github.com/seznam/jailoc/compare/v1.3.0...v1.4.0) (2026-03-26)


### Features

* attach watchdog with container restart detection ([dbab710](https://github.com/seznam/jailoc/commit/dbab7102ed3923f0e23fdf6d667d6ff857035e28))
* cobra subcommands — up, down, attach, status, config, logs, add ([f3d5301](https://github.com/seznam/jailoc/commit/f3d530154b10d931aa9902e323e54751f1de62a0))
* defaults section, env/env_file support, and allowed-files write-ahead ([6207da8](https://github.com/seznam/jailoc/commit/6207da8d8caa991f4814d15741e8affdef871445))
* dockerfile URL presets with HTTP fetch, content-hash tagging, and validation ([3da02ce](https://github.com/seznam/jailoc/commit/3da02ceb55105db7b16ca7262b7c98471e7eb705))
* entrypoint firewall rules, integration tests, README rewrite, and cleanup ([c8b3346](https://github.com/seznam/jailoc/commit/c8b334627ce99659427e3cb02c851009c124dc00))
* Go CLI core — embedded assets, config, workspace, compose, docker packages ([8b6e3a8](https://github.com/seznam/jailoc/commit/8b6e3a8d41dbb227458225034787955d97bb58b1))
* initial sandbox with Dockerfile, DinD sidecar, network isolation, and firewall ([83eb4be](https://github.com/seznam/jailoc/commit/83eb4bed5cb39222a20f1ad64760d3f9d3b699f5))
* mount path validation and access modes (remote/exec) with CLI flags ([979ade2](https://github.com/seznam/jailoc/commit/979ade2815ef82b9b0872c4fa939065f5c69101a))
* progress messages and compose event processor ([f6b3b4f](https://github.com/seznam/jailoc/commit/f6b3b4f60db0ec66500353964f92c73e362bce62))
* up command with 4-tier image resolution, CWD detection, and GoReleaser ([ab251c7](https://github.com/seznam/jailoc/commit/ab251c71cb1c93b1912d70573f15028763db1800))
* working_dir, mermaid diagrams, access modes page, and jailOC rebrand ([25bcfe8](https://github.com/seznam/jailoc/commit/25bcfe89895d60c00f9465df7685d09678928fe5))
* workspace image field, [image]→[base] rename, and mutual exclusivity ([4952608](https://github.com/seznam/jailoc/commit/495260875b012b0aab7381bceb05f35299d0b1ae))


### Bug Fixes

* add MIT license and contributing guide ([f7390f0](https://github.com/seznam/jailoc/commit/f7390f0b65f811cd036bdf1483aa7f319b87944e))
* DAC_READ_SEARCH cap, Diátaxis docs restructure, exec mode, and path argument ([78346bc](https://github.com/seznam/jailoc/commit/78346bcc3bc3688b5cea77d9b442ad2e6697fa2c))
* DinD networking, lint issues, progress display, readiness poll, and CVE patches ([cef4531](https://github.com/seznam/jailoc/commit/cef4531e91fe0149849798684df5cf54ee78639e))


### Code Refactoring

* rename module to github.com/seznam/jailoc and migrate to Docker Go SDKs ([a0909b8](https://github.com/seznam/jailoc/commit/a0909b833634b60029b2025992b95e2c21d5c697))
* two-tier base+overlay image resolution replacing cascade model ([377183a](https://github.com/seznam/jailoc/commit/377183a6407002dadd382632a6a3b71dca0097b5))


### Documentation

* Czech translation, character easter eggs, and content polish ([1fbe0b9](https://github.com/seznam/jailoc/commit/1fbe0b907d0fe71ff7722e3ddfb4bfb00dfe9870))
* landing page, branding, threat model, and Mermaid diagram polish ([4801ae0](https://github.com/seznam/jailoc/commit/4801ae0711f95f6270dfd87277e61684f9ed1f99))
* scaffold MkDocs site with pages, dark theme, and downloads ([c700cf5](https://github.com/seznam/jailoc/commit/c700cf56754b0cc3b5185a45e3055034b5cfe766))


### Miscellaneous

* migrate CI, GoReleaser, and docs to GitHub ([4ea251a](https://github.com/seznam/jailoc/commit/4ea251a461fe009313337f46f0652ded0644f99f))
* release 1.3.0 with release-please ([b4c7a1d](https://github.com/seznam/jailoc/commit/b4c7a1d1f445ec6873f0eb4c45919246ecb0c5f9))
* release v1.1.0 ([df47b07](https://github.com/seznam/jailoc/commit/df47b07d6f7483c49a0dbe3d8180e532e30c975c))
* release v1.2.0 ([90736ce](https://github.com/seznam/jailoc/commit/90736ce29449f7623f44f7a8a05a30e54ac45031))


### Tests

* unit test coverage for cmd, compose, config, workspace, and docker ([d7ea5a8](https://github.com/seznam/jailoc/commit/d7ea5a82844763ebece8d198520d7da9ecc16a1a))

## [1.3.0](https://github.com/seznam/jailoc/compare/v1.2.0...v1.3.0) (2026-03-26)

### Features

* add release-please for automated releases ([b10e2f2](https://github.com/seznam/jailoc/commit/b10e2f24f217f2e115242e73b629a2e63bca5d70))

### Bug Fixes

* add MIT license and contributing guide ([f7390f0](https://github.com/seznam/jailoc/commit/f7390f0b65f811cd036bdf1483aa7f319b87944e))

### Miscellaneous

* migrate CI, GoReleaser, and docs to GitHub ([4ea251a](https://github.com/seznam/jailoc/commit/4ea251a461fe009313337f46f0652ded0644f99f))

## [1.2.0](https://github.com/seznam/jailoc/compare/v1.1.0...v1.2.0) (2026-03-26)

### Features

* dockerfile URL presets with HTTP fetch, content-hash tagging, and validation ([3da02ce](https://github.com/seznam/jailoc/commit/3da02ceb55105db7b16ca7262b7c98471e7eb705))
* defaults section, env/env_file support, and allowed-files write-ahead ([6207da8](https://github.com/seznam/jailoc/commit/6207da8d8caa991f4814d15741e8affdef871445))
* attach watchdog with container restart detection ([dbab710](https://github.com/seznam/jailoc/commit/dbab7102ed3923f0e23fdf6d667d6ff857035e28))
* workspace image field, [image]→[base] rename, and mutual exclusivity ([4952608](https://github.com/seznam/jailoc/commit/495260875b012b0aab7381bceb05f35299d0b1ae))
* progress messages and compose event processor ([f6b3b4f](https://github.com/seznam/jailoc/commit/f6b3b4f60db0ec66500353964f92c73e362bce62))

### Bug Fixes

* DAC_READ_SEARCH cap, Diátaxis docs restructure, exec mode, and path argument ([78346bc](https://github.com/seznam/jailoc/commit/78346bcc3bc3688b5cea77d9b442ad2e6697fa2c))

### Code Refactoring

* two-tier base+overlay image resolution replacing cascade model ([377183a](https://github.com/seznam/jailoc/commit/377183a6407002dadd382632a6a3b71dca0097b5))

### Documentation

* landing page, branding, threat model, and Mermaid diagram polish ([4801ae0](https://github.com/seznam/jailoc/commit/4801ae0711f95f6270dfd87277e61684f9ed1f99))

## [1.1.0](https://github.com/seznam/jailoc/releases/tag/v1.1.0) (2026-03-23)

### Features

* initial sandbox with Dockerfile, DinD sidecar, network isolation, and firewall ([83eb4be](https://github.com/seznam/jailoc/commit/83eb4bed5cb39222a20f1ad64760d3f9d3b699f5))
* Go CLI core — embedded assets, config, workspace, compose, docker packages ([8b6e3a8](https://github.com/seznam/jailoc/commit/8b6e3a8d41dbb227458225034787955d97bb58b1))
* cobra subcommands — up, down, attach, status, config, logs, add ([f3d5301](https://github.com/seznam/jailoc/commit/f3d530154b10d931aa9902e323e54751f1de62a0))
* up command with 4-tier image resolution, CWD detection, and GoReleaser ([ab251c7](https://github.com/seznam/jailoc/commit/ab251c71cb1c93b1912d70573f15028763db1800))
* entrypoint firewall rules, integration tests, README rewrite, and cleanup ([c8b3346](https://github.com/seznam/jailoc/commit/c8b334627ce99659427e3cb02c851009c124dc00))
* mount path validation and access modes (remote/exec) with CLI flags ([979ade2](https://github.com/seznam/jailoc/commit/979ade2815ef82b9b0872c4fa939065f5c69101a))
* working_dir, mermaid diagrams, access modes page, and jailOC rebrand ([25bcfe8](https://github.com/seznam/jailoc/commit/25bcfe89895d60c00f9465df7685d09678928fe5))

### Bug Fixes

* DinD networking, lint issues, progress display, readiness poll, and CVE patches ([cef4531](https://github.com/seznam/jailoc/commit/cef4531e91fe0149849798684df5cf54ee78639e))

### Code Refactoring

* rename module to github.com/seznam/jailoc and migrate to Docker Go SDKs ([a0909b8](https://github.com/seznam/jailoc/commit/a0909b833634b60029b2025992b95e2c21d5c697))

### Tests

* unit test coverage for cmd, compose, config, workspace, and docker ([d7ea5a8](https://github.com/seznam/jailoc/commit/d7ea5a82844763ebece8d198520d7da9ecc16a1a))

### Documentation

* scaffold MkDocs site with pages, dark theme, and downloads ([c700cf5](https://github.com/seznam/jailoc/commit/c700cf56754b0cc3b5185a45e3055034b5cfe766))
* Czech translation, character easter eggs, and content polish ([1fbe0b9](https://github.com/seznam/jailoc/commit/1fbe0b907d0fe71ff7722e3ddfb4bfb00dfe9870))
