# [1.2.0](https://github.com/seznam/jailoc/compare/1.1.0...1.2.0) (2026-03-26)


### Bug Fixes

* address code review findings from MR !11 ([d590552](https://github.com/seznam/jailoc/commit/d590552bf0fe95bf9f865bfca62ff2062a9409bd))
* **ci:** flatten else-if and remove unused pullImage to fix golangci-lint ([cadbc3f](https://github.com/seznam/jailoc/commit/cadbc3fac537bc7ce139fbfd6fa317bea24c1a2c))
* **ci:** install SZN CA certs in pages job to fix pip SSL errors ([c6b1f66](https://github.com/seznam/jailoc/commit/c6b1f6635c9a07c883c56aab719067ec1e448c90))
* **ci:** set pages job stage to production so goreleaser needs reference resolves ([05d2c5e](https://github.com/seznam/jailoc/commit/05d2c5e7cf0083ac38212887a152b9a3aaf47a05))
* **cmd:** exec mode attaches to server instead of starting standalone client ([1d0a08b](https://github.com/seznam/jailoc/commit/1d0a08b2c27fa27b034584effb5d790cdd0bef77))
* **cmd:** stop attach when opencode container restarts ([a0c0340](https://github.com/seznam/jailoc/commit/a0c0340354597025eb61e08a396407e7d8883f35))
* **compose:** add DAC_READ_SEARCH cap to fix chown on restricted cache dirs ([398cf29](https://github.com/seznam/jailoc/commit/398cf296ca397a0ac47572a979e46be520d54a7b))
* **compose:** clean stale containerd state on DinD start to prevent crash loop ([bedd881](https://github.com/seznam/jailoc/commit/bedd8815c9fe3f42582c64636e882a522c882b22))
* **config,docker:** reject empty-host dockerfile URLs and trim whitespace before fetch ([92c4ac8](https://github.com/seznam/jailoc/commit/92c4ac8f8d7d73bf2377c94d96f02bbfe6411a8b))
* **config:** defer close, dedup env_file paths, remove dead test code ([426f14f](https://github.com/seznam/jailoc/commit/426f14fd97a75d9c2c45d1267edf9208bc8e64de))
* **config:** reject whitespace-only workspace image and add image field round-trip tests ([da1cf9a](https://github.com/seznam/jailoc/commit/da1cf9adc16f4fce1472166134777596f0575a97))
* **config:** suppress gosec G304 [secure] positives in WriteAllowedFiles tests ([e25b5fa](https://github.com/seznam/jailoc/commit/e25b5faf389b5c107c97c9f27fbb622016ec8b99))
* **config:** write allowed-hosts and allowed-networks files before container start ([954ce6a](https://github.com/seznam/jailoc/commit/954ce6a8511354893cdc3550ffc153b8ca2a4be7))
* **docker:** suppress errcheck for resp.Body.Close in fetchDockerfile ([c7170dc](https://github.com/seznam/jailoc/commit/c7170dc325d0be292de39b4d044c0bb3eb6b50c2))
* **docker:** suppress gosec SSRF [secure] positive on validated URL fetch ([6967829](https://github.com/seznam/jailoc/commit/69678293d67f1fd299fc2f1d267626ed1e582928))
* **docker:** use content-hash tag for preset image and fix size limit check ([778e9d2](https://github.com/seznam/jailoc/commit/778e9d25aee892013c03f019287fd8a1c40f6113))
* **lint:** resolve errcheck, gosec, and staticcheck findings ([7ceafee](https://github.com/seznam/jailoc/commit/7ceafee96c8fb5dcf9abceea337a1ccc3da09c1d))
* **lint:** suppress gosec [secure] positives for validated paths and temp files ([b0b2268](https://github.com/seznam/jailoc/commit/b0b226891283d9ac62ec72799b843e74e944c91d))
* **lint:** suppress remaining gosec G703 [secure] positive in workspace test ([0283cd5](https://github.com/seznam/jailoc/commit/0283cd58e7157a0fcbd98ad783d18e71a3e7d8ea))


### Features

* **attach:** add workspace watchdog to detach when backend stops ([b651dfa](https://github.com/seznam/jailoc/commit/b651dfa9ea3922ed3d960d464c41dfad055b19c2))
* **cmd:** accept optional path argument and warn on broad paths ([0b634cb](https://github.com/seznam/jailoc/commit/0b634cba62fe84bf107d65e7c972152f5a6e8714))
* **cmd:** add granular progress logging and compose event processor ([73b1749](https://github.com/seznam/jailoc/commit/73b174968036a64c3ae0a5c26bebe32a1e57aa1e))
* **cmd:** add progress messages for user feedback during operations ([2f59e86](https://github.com/seznam/jailoc/commit/2f59e86b130312330b1ddbd4e183e6ad156033e0))
* **cmd:** wire workspace-level preset into ResolveAndLayerImage ([66e5db5](https://github.com/seznam/jailoc/commit/66e5db5b56c2aa29e5eed09e14af17dec779a236))
* **compose:** render user env vars in docker-compose template ([69e542c](https://github.com/seznam/jailoc/commit/69e542c822540cbdadf5bdd392de7b4595acd1f0))
* **compose:** wire env vars from workspace to compose params ([2fdc75b](https://github.com/seznam/jailoc/commit/2fdc75b427692c9949395ad7dd959b47d89a377a))
* **config:** add Defaults section and env/env_file fields to Workspace ([a36c27a](https://github.com/seznam/jailoc/commit/a36c27a76db6eda9105928caf61da089b9e933f8))
* **config:** add Docker .env file parser ([69ab880](https://github.com/seznam/jailoc/commit/69ab880a04e5be6a22a71e0868d61a1e023b883e))
* **config:** add dockerfile URL field to ImageConfig and Workspace ([a575795](https://github.com/seznam/jailoc/commit/a575795ebce920a37e120de1fbc10ddcd4b97514))
* **config:** add image field to Workspace and Defaults with mutual exclusivity validation ([e17d580](https://github.com/seznam/jailoc/commit/e17d580f29784e40d44b94bf617d8a29cbabda8f))
* **config:** merge global defaults for allowed_hosts and allowed_networks ([9c602b2](https://github.com/seznam/jailoc/commit/9c602b2c6ceb251d509134f2b0ec7ace6a06da03))
* **config:** validate env format, reserved keys, and env_file paths ([1ba21c0](https://github.com/seznam/jailoc/commit/1ba21c045c0e2c82cdf4099cb5bc56c3995a122e))
* **docker:** add HTTP Dockerfile fetch function ([d9d6702](https://github.com/seznam/jailoc/commit/d9d67025342a8d8973ace123106f9cc43c04dfae))
* **docker:** add preset image build from fetched Dockerfile ([2131ffc](https://github.com/seznam/jailoc/commit/2131ffc64956fdb737b141c883772f9cca809c70))
* **docker:** integrate preset as step 0 in ResolveImage cascade ([51f6bb0](https://github.com/seznam/jailoc/commit/51f6bb040ee95ce8418ec8b6f51f1c844184f76f))
* **docker:** short-circuit image resolution for workspace image and defaults.image as base ([9396fe3](https://github.com/seznam/jailoc/commit/9396fe3f78e2b521c1f18d6b83f170181766907c))
* **workspace:** propagate dockerfile field through resolution ([a6bcd15](https://github.com/seznam/jailoc/commit/a6bcd1549bcf1de2ff7cc02d0918a7b7e59de87a))
* **workspace:** resolve and merge env vars from defaults, env_file, and workspace ([8df07a3](https://github.com/seznam/jailoc/commit/8df07a3c62ca28ed49f249aba50f52f6f018a080))

# [1.1.0](https://github.com/seznam/jailoc/compare/1.0.1...1.1.0) (2026-03-23)


### Bug Fixes

* **ci:** correct python image path in pages job ([38beb21](https://github.com/seznam/jailoc/commit/38beb2145938100ca3725e81abee034e5041e981))
* **ci:** correct python image path to upstream/python ([6a5ab90](https://github.com/seznam/jailoc/commit/6a5ab90471678fc01f18e34f53b90adfb13777f8))
* **ci:** exclude Go dirs from setuptools package discovery ([9263212](https://github.com/seznam/jailoc/commit/9263212222b617e51dfec408c4af72b0dce4c569))
* **ci:** preprocess {{ version }} in docs before zensical build ([cc56dcb](https://github.com/seznam/jailoc/commit/cc56dcbc23dc3d6e30dd0068abb14b902aa356a1))
* **ci:** restrict pages job to tag pipelines only ([0174dde](https://github.com/seznam/jailoc/commit/0174dde2dd8e215add8db2601f2a58a626af4aca))
* **ci:** use pipe delimiter in sed to handle branch slashes ([ff0fdf5](https://github.com/seznam/jailoc/commit/ff0fdf56b91176c58bc57b895540358f76c6f653))


### Features

* **ci:** enable changelog generation via semantic-release ([f484e38](https://github.com/seznam/jailoc/commit/f484e3828f30700f63dd06ea4136b01733d65176))
* **compose:** set working_dir to first workspace path for opencode service ([d5b175e](https://github.com/seznam/jailoc/commit/d5b175e8c1affbeb38a63b2853796ced12ab7ffd))
