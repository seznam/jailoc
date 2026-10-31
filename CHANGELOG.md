# Changelog

## [2.0.0-rc](https://github.com/seznam/jailoc/compare/v1.19.0...v2.0.0-rc) (2026-10-31)


### ⚠ BREAKING CHANGES

* **config:** enable_docker defaults to false. Set enable_docker = true in [defaults] or a workspace to keep Docker inside the VM.
* **image:** the embedded base image no longer contains Docker or nftables. Workspace overlays that run the docker CLI during the build need enable_docker on; a [base] dockerfile without nft on PATH needs apt-get for the Docker layer.
* **agent:** OpenCode is no longer part of the embedded base image; jailoc installs it in a separate layer. A `[base] dockerfile` needs npm on PATH, and an image used directly (`image`, or `defaults.image` without a workspace `dockerfile`) must contain the agent binary. The default mount of ~/.claude/transcripts is removed.
* the Docker backend and remote mode are gone, copies is renamed to snapshots, and env secrets are no longer written to /run/secrets.

### Features

* **agent:** add OpenCode 2 harness and workspace indicator for every agent ([d2baf9e](https://github.com/seznam/jailoc/commit/d2baf9e09b0d2896c9503b639c229f3b9380868e))
* **agent:** add per-agent mounts and state profiles ([5e7d2dc](https://github.com/seznam/jailoc/commit/5e7d2dcb3e1204805829158f610808399d66c9f9))
* **agent:** derive custom agents and override agent versions ([896d7b7](https://github.com/seznam/jailoc/commit/896d7b7d1b72191eeb86cc4c02405bed8d14cfa2))
* **agent:** run Claude Code, Copilot CLI and OmO besides OpenCode ([cba7c65](https://github.com/seznam/jailoc/commit/cba7c65b92508f51082efb04de7b1bdd2777d82a))
* **cmd:** add jailoc list and show agent, resources, sessions and drift in status ([72a28cd](https://github.com/seznam/jailoc/commit/72a28cd072e83963d552b0eb96f8aa97b74ab17e))
* **config:** make enable_docker opt-in ([7b56f4f](https://github.com/seznam/jailoc/commit/7b56f4f6d02513c1f5bc4b40aa2bedcbcd64ed3a))
* **docker:** build images with BuildKit and cache apt/npm downloads ([c84294f](https://github.com/seznam/jailoc/commit/c84294f32ca4ef8fad3224de7f9dc2b1b59f2ec5))
* **docker:** cache apt downloads in the Docker layer ([e173042](https://github.com/seznam/jailoc/commit/e173042ff13945e59121a1c18022853f20a64bac))
* **docker:** remove superseded jailoc images after import ([2285165](https://github.com/seznam/jailoc/commit/22851656bec635c80659c492bfbfba6c80d27074))
* **guest:** enable zram swap and periodic memory reclaim ([58ea081](https://github.com/seznam/jailoc/commit/58ea0818c821cde65f6454d93f5f114c4d24f9cb))
* **guest:** make the agent the least preferred OOM victim in its session ([2f9eccd](https://github.com/seznam/jailoc/commit/2f9eccdcfbcc179f9c049de55af38b0a3ba71000))
* **image:** install Docker only for workspaces with enable_docker ([67730e0](https://github.com/seznam/jailoc/commit/67730e023ecbcc41459f67b9b0d563491c9c844e))
* make jailoc v2 VM-only ([c2377b8](https://github.com/seznam/jailoc/commit/c2377b8cfd62c1793daece04a774188c4145e365))
* **netbroker:** add userspace network broker and supervision ([95e718b](https://github.com/seznam/jailoc/commit/95e718b417eb9120e89ea21eebca61cfdf841fb1))
* **netbroker:** record TLS server names and HTTP requests of allowed connections ([d05f6e4](https://github.com/seznam/jailoc/commit/d05f6e4d59fecb26e5b2972650d10c42853feed0))
* **netbroker:** route guest IPv6 egress through the broker policy ([c1cf406](https://github.com/seznam/jailoc/commit/c1cf4063a46a5485f456190462aac512e67da939))
* **session:** make detach keys configurable, default to ctrl-b,d ([05a6cb4](https://github.com/seznam/jailoc/commit/05a6cb427fb7a5563092a0ae53eae6735806a0a8))
* **session:** show that the agent is starting ([fd1d04f](https://github.com/seznam/jailoc/commit/fd1d04f000f283f5244871f415f8b34d7620921d))
* **vm:** add experimental VM backend for workspaces ([f9dc9d8](https://github.com/seznam/jailoc/commit/f9dc9d869cd72e7a332a9646aebb0c6dfdb6bd6f))
* **vm:** add jailoc migrate to move all workspaces to the VM backend ([2a67940](https://github.com/seznam/jailoc/commit/2a679406321d83116d27e07c135413a1ce6d9bd4))
* **vm:** add read-only copies for files and directories ([f96e9d1](https://github.com/seznam/jailoc/commit/f96e9d1c62f6ef4b65ef89c55f15b56aa6d35ece))
* **vm:** add traffic, strace, exec and shell commands ([314461b](https://github.com/seznam/jailoc/commit/314461bf761a93ae71c2fb66517f027020a2e71a))
* **vm:** apply configuration changes to running VM workspaces ([5f1fcc8](https://github.com/seznam/jailoc/commit/5f1fcc882cc3df9a83ee722cc73cea756708dbcf))
* **vm:** build workspace images with the host Docker daemon when available ([d14b475](https://github.com/seznam/jailoc/commit/d14b4750f893061be9fa4d54e2459d085e21f869))
* **vm:** fall back to the builder VM when host Docker is older than Engine 25 ([6b082ec](https://github.com/seznam/jailoc/commit/6b082ec8ba8c270e53614c8be776bf0d3c436d1e))
* **vm:** idmap the root filesystem and shares on Linux hosts ([58f9f3a](https://github.com/seznam/jailoc/commit/58f9f3ab0498aae8977ce862db5a1dba08fcc57a))
* **vm:** let the agent use sudo inside the workspace VM ([b4332e5](https://github.com/seznam/jailoc/commit/b4332e50621a8ba74c784568f82e4d53c4af5aab))
* **vm:** log the duration of each image and boot step ([585a069](https://github.com/seznam/jailoc/commit/585a069e6eae0722cacf4e16e78bfe7f9418025b))
* **vm:** persist OpenCode state across workspace VM restarts ([a332c39](https://github.com/seznam/jailoc/commit/a332c3968fb7a52ba45c827cf6edae16b701ed67))
* **vm:** pin the linux/amd64 VM runtime ([29b261e](https://github.com/seznam/jailoc/commit/29b261e106a3c7a0388bc25d2a994f60c3f86880))
* **vm:** pin VM runtime v0.0.41-jailoc.6 with zram-capable firmware ([d6c0a13](https://github.com/seznam/jailoc/commit/d6c0a134f72adf3f97f8efa5262ac137cd212825))
* **vm:** protect Git hooks and config with protect_git ([62e75d2](https://github.com/seznam/jailoc/commit/62e75d2685b2de080f9a5311bdf2469af0c20585))
* **vm:** prune unused workspace images from the image cache ([a7a848b](https://github.com/seznam/jailoc/commit/a7a848b1e5954a3cd3f6aeb8a60714f1104c804c))
* **vm:** run agents in detachable sessions ([b386f5f](https://github.com/seznam/jailoc/commit/b386f5f48a2e07ec0293a7ebe8d1935507e0a715))
* **vm:** run Docker and publish ports in workspace VMs ([a7944b2](https://github.com/seznam/jailoc/commit/a7944b2ae0a0f8e9dd267faee16e9350b7b845e6))
* **vm:** stream docker save and skip cached layers on import ([e1541b1](https://github.com/seznam/jailoc/commit/e1541b121c85fc57e2c81f65c96bae9eb2aa0db4))
* **vm:** support SSH agent, filtered DNS, CA bundle and memory limits ([a2f52e9](https://github.com/seznam/jailoc/commit/a2f52e9393e77b62d5ba170a42ad529f4b886094))


### Bug Fixes

* **cmd:** allow workspaces without shared paths ([9aed390](https://github.com/seznam/jailoc/commit/9aed390c3e2b977106c7aa2243d8e47cb569aba4))
* **cmd:** escape terminal controls in inspection output ([7041197](https://github.com/seznam/jailoc/commit/704119745bac15f8fb087ffed02b133f98835b2c))
* **cmd:** follow a replaced log file in logs -f ([0aec3d5](https://github.com/seznam/jailoc/commit/0aec3d58af35a9aadcc35e00304dd9ac18724dbc))
* **cmd:** protect git directories that gitfiles and submodules point at ([1f485d5](https://github.com/seznam/jailoc/commit/1f485d54f12f9771d7630492e5dd3be592129c02))
* **cmd:** protect git metadata through worktree pointers and symlink roots ([4ae3b67](https://github.com/seznam/jailoc/commit/4ae3b677949504ce87e0e0d83417d620929ff805))
* **cmd:** resolve symlinked snapshot source directories ([dfbed0f](https://github.com/seznam/jailoc/commit/dfbed0ff21ff93bb2dc8ee1abe7b38f50ab3b396))
* **config:** keep an explicitly empty agents args list when rewriting config ([af93004](https://github.com/seznam/jailoc/commit/af9300491cf1901ecbd825d3839a06b6e978b9a8))
* **deps:** bump brace-expansion from 2.1.4 to 2.1.7 ([#249](https://github.com/seznam/jailoc/issues/249)) ([526d3a6](https://github.com/seznam/jailoc/commit/526d3a62415610d206e7345a4ec2c1d95969d9b9))
* **deps:** bump certifi ([41ad53b](https://github.com/seznam/jailoc/commit/41ad53bf96b7832e0744086582ec46031d3a9a67))
* **deps:** bump goreleaser/goreleaser-action ([#251](https://github.com/seznam/jailoc/issues/251)) ([d8df5e8](https://github.com/seznam/jailoc/commit/d8df5e89c6ce64fe8376e6c0a02e6e5780845052))
* **deps:** bump the all group across 1 directory with 4 updates ([#250](https://github.com/seznam/jailoc/issues/250)) ([feb956a](https://github.com/seznam/jailoc/commit/feb956aeb2b63801dfbd68ab61fc589f716eb231))
* **deps:** bump the uv group across 1 directory with 2 updates ([#248](https://github.com/seznam/jailoc/issues/248)) ([5f81a1d](https://github.com/seznam/jailoc/commit/5f81a1dccc414d62bedc2e0a4116bbe39eabdaae))
* **deps:** track upstream go-microvm main with the remaining fork patches ([969488f](https://github.com/seznam/jailoc/commit/969488f98e5961370775ddb400b5a8960516fe7f))
* **docker:** attach a BuildKit session to image builds ([0b21076](https://github.com/seznam/jailoc/commit/0b210765a9e9b81a8e4897502808cf7fe8baa5a0))
* **docker:** prune images only when no other jailoc up is running ([d8c7c1f](https://github.com/seznam/jailoc/commit/d8c7c1f1b83caa6d56879b91e9bb74567f3ec64d))
* **docker:** pull the host platform when the cached image is for another ([cad9ee7](https://github.com/seznam/jailoc/commit/cad9ee7638de88281ec2378732df48aaf913e5fd))
* **docker:** skip the BuildKit default ARG check in layer Dockerfiles ([be823b7](https://github.com/seznam/jailoc/commit/be823b7a5992136c6395ecb3b76829b87bb4f0b1))
* **guestbin:** build jailoc-guest without VCS stamp to avoid needless restarts ([05f1520](https://github.com/seznam/jailoc/commit/05f1520f86286305098063eafaa8d6bae32e90ec))
* **guest:** keep the periodic reclaim to file cache ([64f97f9](https://github.com/seznam/jailoc/commit/64f97f9c82d08d0f7fe3865a902de0e791b6e932))
* **guest:** mount shares inside persistent home directories on top ([8bfdd6e](https://github.com/seznam/jailoc/commit/8bfdd6e1674dc36a127d5959a3e1f82da753568c))
* **guest:** share one session between concurrent opens in a directory ([300ea38](https://github.com/seznam/jailoc/commit/300ea38f9e99d5da962672a5c9072aeae940291c))
* **netbroker:** close open service connections when a service is removed ([960d52a](https://github.com/seznam/jailoc/commit/960d52a11bfc2ceb0cfd3c4bee8f595dab5bc646))
* **netbroker:** close removed runtime listeners before reload returns ([4b158bd](https://github.com/seznam/jailoc/commit/4b158bd68160d05f42a96cb47125d91e54c781ca))
* **nix:** bump nixpkgs for Go 1.26.8 and update vendorHash ([94e0c40](https://github.com/seznam/jailoc/commit/94e0c4090343679cd336b97e9b2e22d3ac462568))
* **nix:** embed jailoc-guest in the Nix package ([600ded1](https://github.com/seznam/jailoc/commit/600ded1040b135cdea349ed4ba948de59c63d1ca))
* **nix:** Update `vendorHash` on Dependabot PRs ([#245](https://github.com/seznam/jailoc/issues/245)) ([5e4e39e](https://github.com/seznam/jailoc/commit/5e4e39e8bee5bbc62234b6303f9aa8672209b956))
* **session:** decode kitty key events when matching detach keys ([5239a3d](https://github.com/seznam/jailoc/commit/5239a3d59f8f12b0ccf0e34269e3bb762161d955))
* **session:** match detach keys in extended keyboard modes ([99b2207](https://github.com/seznam/jailoc/commit/99b22075883d88b1cc6d3fd76f226483a554f9a6))
* **vm:** confine the VM runner with Landlock on Linux ([525cdf8](https://github.com/seznam/jailoc/commit/525cdf82ae3cc9c55d9f14fe46b8298bf7e62e02))
* **vm:** do not require world-readable secret files ([56afb87](https://github.com/seznam/jailoc/commit/56afb8712ef2cdf315e7e91ffbad59eb5957474e))
* **vm:** honor the image PATH in workspace VMs ([c42994d](https://github.com/seznam/jailoc/commit/c42994d1281c873be039123e853b9adf58a8340c))
* **vm:** include [defaults] allowed hosts and networks in the broker policy ([f69525a](https://github.com/seznam/jailoc/commit/f69525a4df66cc0200518f3339d02050d95373ab))
* **vm:** keep session exit status from the guest zombie reaper ([8323897](https://github.com/seznam/jailoc/commit/8323897116b90274ef75bdd25c6de1b74a837d48))
* **vm:** keep setuid bits in layer-cached workspace images ([77e08de](https://github.com/seznam/jailoc/commit/77e08de481c6bd4af8cbb56677bbedaac8069b14))
* **vm:** keep setuid bits in workspace images on Linux hosts ([31eb19e](https://github.com/seznam/jailoc/commit/31eb19e7185c7f127faff5875ee029f40ab4799a))
* **vm:** patch libkrun for 19 device interrupts on Linux x86_64 ([fdad514](https://github.com/seznam/jailoc/commit/fdad514e83f906b98eb6747c289df8d56d2ff62b))
* **vm:** pin the VM runtime with patched Linux libkrun ([e7ee01f](https://github.com/seznam/jailoc/commit/e7ee01f2c7507450ca4e6ed486810ee14ee8ede9))
* **vm:** record the session holder PID before accepting clients ([08e6c07](https://github.com/seznam/jailoc/commit/08e6c074032e9ec48ddadad3ac60c284ed8b6583))
* **vm:** repin go-microvm to discard old-format layers before import ([263e613](https://github.com/seznam/jailoc/commit/263e613823af40dda0845d34d4a953e684d28272))
* **vm:** report session start failures immediately ([8deab31](https://github.com/seznam/jailoc/commit/8deab31d99fe5629a76d4ffc9275729056b4226c))
* **vm:** report unpack errors, honour cancellation and skip cached blobs unread ([36eb8c9](https://github.com/seznam/jailoc/commit/36eb8c9a48a4985f863c83b60baec1b88b99915c))
* **vm:** reset terminal modes after a VM session ends ([64e6de6](https://github.com/seznam/jailoc/commit/64e6de6243b8d3b92928d8e573708db38b15552f))
* **vm:** route libkrun device interrupts above 15 on Linux x86_64 ([41ae170](https://github.com/seznam/jailoc/commit/41ae1701ae16d350eb1d8aff7b62539e0c2b3f7d))
* **vm:** run the agent with the host user's IDs on Linux ([6fbfa8a](https://github.com/seznam/jailoc/commit/6fbfa8ac8aa90cc1e59f6981e7673023060ce017))
* **vm:** stop the builder VM when its data disk cannot be mounted ([41dfd21](https://github.com/seznam/jailoc/commit/41dfd21f17c7701ccaed2cc0b4b3176a4832df63))
* **vm:** trust the CA bundle before the builder VM downloads packages ([fc58be6](https://github.com/seznam/jailoc/commit/fc58be6666f8e393e49bb3ab74bcea3bb54477d3))
* **vm:** use one internal share per workspace VM ([419c032](https://github.com/seznam/jailoc/commit/419c032d9c6b1827fbd8c0e6aad6080a91b89250))
* **vm:** use simplified virtio-fs ownership only on macOS ([70b9eaa](https://github.com/seznam/jailoc/commit/70b9eaa2d58204f7b852e723a2129ea571fdca18))
* **vm:** wait for FUSE INIT before idmapping a share ([b5db579](https://github.com/seznam/jailoc/commit/b5db57907ba364439c7f51cad7c1664c3f76c11d))


### Performance Improvements

* **netbroker:** use a 64 KiB MTU and buffered NIC reads ([ff68918](https://github.com/seznam/jailoc/commit/ff689182d0676835716a210014ef0fb1bd3582be))
* **vm:** prune composed-layer snapshots with unused images ([21d5b4d](https://github.com/seznam/jailoc/commit/21d5b4df61dd288bcc8bef8834ddaeaa3ecf7540))


### Documentation

* build from source instead of go install ([7308c20](https://github.com/seznam/jailoc/commit/7308c206325ca194b7e380617e8f53ed91d0ee7d))
* describe detach keys in extended keyboard modes ([c0189ef](https://github.com/seznam/jailoc/commit/c0189efb874635c89a8ac3267b3f3d70a9bad9a5))
* describe jailoc v2 as VM-only ([eb08f4d](https://github.com/seznam/jailoc/commit/eb08f4ddfa7df7cfb171bb2228fbea77c8d2633d))
* describe workspace boundary fixes ([9f6d5b9](https://github.com/seznam/jailoc/commit/9f6d5b9acf03ad4a64d48f63c60a3200d2749053))
* **dev:** note enable_docker for the Docker integration tests ([0a3d8c9](https://github.com/seznam/jailoc/commit/0a3d8c9a6b037d629ba8f27aa856ae664ca66271))
* tighten Diátaxis structure, drop v1 mentions, warn about shared profiles ([3521a27](https://github.com/seznam/jailoc/commit/3521a279554953dc6491f9267544d725018d7484))
* **vm:** list VM backend restrictions and image cache pruning ([19f48ff](https://github.com/seznam/jailoc/commit/19f48ffcc6612a36de34b4ef60b4e667537af6a5))


### Tests

* **vm:** enable Docker in the integration workspace ([df93ebd](https://github.com/seznam/jailoc/commit/df93ebd3d6f3dde21ec231eb2dc6767017a0259b))
* **vmintegration:** check a real OOM kill spares the agent ([200a01f](https://github.com/seznam/jailoc/commit/200a01f4e1019f015db586393ae81a45d0ec78b5))
* **vm:** print the runner log when the VM integration test fails ([db1e82f](https://github.com/seznam/jailoc/commit/db1e82f4fc3fcbb4e25495a851380f232dd4705f))

## [1.19.0](https://github.com/seznam/jailoc/compare/v1.18.0...v1.19.0) (2026-09-23)


### Features

* **ca:** forward host CA trust to OpenCode and DinD ([#235](https://github.com/seznam/jailoc/issues/235)) ([fd2db2e](https://github.com/seznam/jailoc/commit/fd2db2ef17db9e8ee5a7d5a0a7ce137d460bfc0d))
* **dns:** add opt-in filtered DNS with configured upstream ([#232](https://github.com/seznam/jailoc/issues/232)) ([81c9a82](https://github.com/seznam/jailoc/commit/81c9a822f0f985d389b626d0a623ba9ae9dd6292))


### Bug Fixes

* **deps:** bump browserslist from 4.28.2 to 4.28.9 ([#225](https://github.com/seznam/jailoc/issues/225)) ([36bb37a](https://github.com/seznam/jailoc/commit/36bb37ad2a3f979aefc5f2b721f4e98796bc80a0))

## [1.18.0](https://github.com/seznam/jailoc/compare/v1.17.0...v1.18.0) (2026-08-19)


### Features

* config-defined docker-compose secrets ([#215](https://github.com/seznam/jailoc/issues/215)) ([3f1d6df](https://github.com/seznam/jailoc/commit/3f1d6df97de94b510058458d3239bf9d2b2b4898))


### Bug Fixes

* **deps:** bump brace-expansion from 2.1.2 to 2.1.4 ([#212](https://github.com/seznam/jailoc/issues/212)) ([309b823](https://github.com/seznam/jailoc/commit/309b823be696bf1a148a080763a5fa53cab9baea))
* **deps:** bump github.com/moby/go-archive from 0.2.0 to 0.3.0 ([#219](https://github.com/seznam/jailoc/issues/219)) ([e954c2b](https://github.com/seznam/jailoc/commit/e954c2b15bd5effc3debb0f5a230cb7f2a770e2a))
* **deps:** bump pymdown-extensions in the uv group across 1 directory ([#214](https://github.com/seznam/jailoc/issues/214)) ([66cd755](https://github.com/seznam/jailoc/commit/66cd7555d093ff2704bb45e2286761a4f5653dda))
* **deps:** bump seroval from 1.5.2 to 1.5.6 ([#211](https://github.com/seznam/jailoc/issues/211)) ([e4610d1](https://github.com/seznam/jailoc/commit/e4610d163b45a5ad985c5af01784c69972abcf28))
* **deps:** bump the go-minor-patch group across 1 directory with 4 updates ([#218](https://github.com/seznam/jailoc/issues/218)) ([e5ada67](https://github.com/seznam/jailoc/commit/e5ada67d676f8071d4ded1bc89968d321b03804d))

## [1.17.0](https://github.com/seznam/jailoc/compare/v1.16.0...v1.17.0) (2026-07-24)


### Features

* **embed:** bump tool versions, drop renovate ([#208](https://github.com/seznam/jailoc/issues/208)) ([926e6a4](https://github.com/seznam/jailoc/commit/926e6a444342b54d4a40bf7b96d36d9cd4ee6b4f))


### Bug Fixes

* **deps:** bump actions/setup-go in the all group across 1 directory ([#200](https://github.com/seznam/jailoc/issues/200)) ([bf2f9a7](https://github.com/seznam/jailoc/commit/bf2f9a7348647a5782fdcd45c36e93fd369fff26))
* **deps:** bump brace-expansion from 2.1.0 to 2.1.2 ([#207](https://github.com/seznam/jailoc/issues/207)) ([3d4494c](https://github.com/seznam/jailoc/commit/3d4494cd9d9ca7f909b419c333f34c96be646685))
* **deps:** bump google.golang.org/grpc from 1.81.1 to 1.82.1 ([#206](https://github.com/seznam/jailoc/issues/206)) ([b7e4cd8](https://github.com/seznam/jailoc/commit/b7e4cd8d02762aadbcb2e9a8635d14e12b6d4a87))
* **deps:** bump idna in the uv group across 1 directory ([#191](https://github.com/seznam/jailoc/issues/191)) ([9192b81](https://github.com/seznam/jailoc/commit/9192b81ab6b205b444927437e518652409f00e9e))
* **deps:** bump the all group across 1 directory with 6 updates ([#205](https://github.com/seznam/jailoc/issues/205)) ([6dfbf1c](https://github.com/seznam/jailoc/commit/6dfbf1c7464b2751521a997a7295eedece98a71a))
* **deps:** bump the go-minor-patch group across 1 directory with 2 updates ([#189](https://github.com/seznam/jailoc/issues/189)) ([3847a0b](https://github.com/seznam/jailoc/commit/3847a0b5a3adf7dbd4415da2d618f4e3277be70f))
* **deps:** bump the go-minor-patch group across 1 directory with 4 updates ([#203](https://github.com/seznam/jailoc/issues/203)) ([6d7b1de](https://github.com/seznam/jailoc/commit/6d7b1de3ef119cca609b2e214e13d1dd45235cee))
* **deps:** bump the go-minor-patch group with 4 updates ([#197](https://github.com/seznam/jailoc/issues/197)) ([a26e592](https://github.com/seznam/jailoc/commit/a26e59226798a318969ba296d1d2ddb34cd4c16c))

## [1.16.0](https://github.com/seznam/jailoc/compare/v1.15.1...v1.16.0) (2026-06-05)


### Features

* **embed:** upgrade pinned tool versions ([#188](https://github.com/seznam/jailoc/issues/188)) ([6be8ff8](https://github.com/seznam/jailoc/commit/6be8ff8be991025b962c1ec54a9437de7d1c3a6c))


### Bug Fixes

* **cmd:** close parent's PTY slave fd to unblock attach on child exit ([#187](https://github.com/seznam/jailoc/issues/187)) ([9f1e1f2](https://github.com/seznam/jailoc/commit/9f1e1f22267019adeb6881a75e34e59907c7fe0e))
* **cmd:** unwrap build errors, remove premature startup message ([#180](https://github.com/seznam/jailoc/issues/180)) ([252cf1f](https://github.com/seznam/jailoc/commit/252cf1fed20080d492e1cbb926300b9ca88ecb77))
* **docker:** prepend ARG BASE to overlay Dockerfiles ([#182](https://github.com/seznam/jailoc/issues/182)) ([62f443b](https://github.com/seznam/jailoc/commit/62f443b9d5cf3cdf708fdb1d56e7ec14f56b86b8))

## [1.15.1](https://github.com/seznam/jailoc/compare/v1.15.0...v1.15.1) (2026-05-22)


### Features

* **nix:** Add `apps` and `formatter` to flake ([#176](https://github.com/seznam/jailoc/issues/176)) ([66878f8](https://github.com/seznam/jailoc/commit/66878f8293a32c8f21216e22cb892018b87dd2f2))


### Miscellaneous

* **deps:** bump goreleaser/goreleaser-action in the all group ([#175](https://github.com/seznam/jailoc/issues/175)) ([132de64](https://github.com/seznam/jailoc/commit/132de64eef360549d435347ee6087ecd7d904de1))

## [1.15.0](https://github.com/seznam/jailoc/compare/v1.14.0...v1.15.0) (2026-05-21)


### Features

* **logging:** add always-on debug file logging ([#172](https://github.com/seznam/jailoc/issues/172)) ([d407654](https://github.com/seznam/jailoc/commit/d4076543b9375cccb6cd2268be58cc647a655ac5))


### Bug Fixes

* **cmd:** enable Windows cross-compilation with PTY fallback ([#161](https://github.com/seznam/jailoc/issues/161)) ([e90baf5](https://github.com/seznam/jailoc/commit/e90baf5040cf62080001317f547de1ff6d484ec7))
* **cmd:** replace creack/pty with go-pty for cross-platform PTY support ([#166](https://github.com/seznam/jailoc/issues/166)) ([5ad0cc1](https://github.com/seznam/jailoc/commit/5ad0cc18a29f3074b9525a12ec21649133ee66c9))
* **config:** use filepath.IsAbs for cross-platform path validation ([#165](https://github.com/seznam/jailoc/issues/165)) ([6baf43e](https://github.com/seznam/jailoc/commit/6baf43e0c60343733cad589f3689c61f37777178))
* **docker:** clarify overlay build status message ([#162](https://github.com/seznam/jailoc/issues/162)) ([f0aa418](https://github.com/seznam/jailoc/commit/f0aa41809b81e17e2ca1281caadf5d0f3613211a))


### Documentation

* **how-to:** add troubleshooting page ([#174](https://github.com/seznam/jailoc/issues/174)) ([948d93e](https://github.com/seznam/jailoc/commit/948d93e6b303802956629e99aa73154271f47fab))

## [1.14.0](https://github.com/seznam/jailoc/compare/v1.13.1...v1.14.0) (2026-05-13)


### Features

* **cmd:** add session continuation and rewrite exit title ([#154](https://github.com/seznam/jailoc/issues/154)) ([66059e3](https://github.com/seznam/jailoc/commit/66059e38f28408a5b4ad9a22b77926ce5257d9c5))


### Bug Fixes

* **config:** mount OpenCode config directories read-write ([#156](https://github.com/seznam/jailoc/issues/156)) ([178f857](https://github.com/seznam/jailoc/commit/178f8574adb60eddfefc83931404ea589f71a65e))

## [1.13.1](https://github.com/seznam/jailoc/compare/v1.13.0...v1.13.1) (2026-04-28)


### Bug Fixes

* **entrypoint:** pre-create OpenCode config .gitignore to prevent EROFS crash ([#148](https://github.com/seznam/jailoc/issues/148)) ([ce08080](https://github.com/seznam/jailoc/commit/ce08080e56acd042584148cb2989ecff086ff5a6))

## [1.13.0](https://github.com/seznam/jailoc/compare/v1.12.2...v1.13.0) (2026-04-24)


### Features

* **config:** add enable_docker option to opt out of DinD sidecar ([#147](https://github.com/seznam/jailoc/issues/147)) ([e523e2e](https://github.com/seznam/jailoc/commit/e523e2efd20eee2d0744a369323319bc2095f671))
* **dind:** switch to rootless Docker daemon for iptables tamper resistance ([#143](https://github.com/seznam/jailoc/issues/143)) ([6248c53](https://github.com/seznam/jailoc/commit/6248c5399b45425fee18a34b4e3cf05c02185f0b))


### Bug Fixes

* **release:** enable force-tag-creation for draft releases ([#125](https://github.com/seznam/jailoc/issues/125)) ([37a2469](https://github.com/seznam/jailoc/commit/37a2469e4735d5630113ca41e78b5f0298738468))


### Documentation

* **network:** document HTTP proxy allowlisting for private networks ([#146](https://github.com/seznam/jailoc/issues/146)) ([6671a6c](https://github.com/seznam/jailoc/commit/6671a6c43c17420debd3ab540819523df577471a))

## [1.12.2](https://github.com/seznam/jailoc/compare/v1.12.1...v1.12.2) (2026-04-23)


### Bug Fixes

* **ci:** gh failure ([096e3ba](https://github.com/seznam/jailoc/commit/096e3bae29d3930f225293543d87bcc0ddb94fcd))
* **ci:** gh failure II ([4ca09cc](https://github.com/seznam/jailoc/commit/4ca09cc3b5fd133c269b1d768a5d28e2032e28bf))

## [1.12.1](https://github.com/seznam/jailoc/compare/v1.12.0...v1.12.1) (2026-04-23)


### Bug Fixes

* **docker:** forward host proxy env vars as build args ([#119](https://github.com/seznam/jailoc/issues/119)) ([441b9eb](https://github.com/seznam/jailoc/commit/441b9eba78a7999dfe13f8bd571d5133ecdbee14))


### Documentation

* **custom-images:** add base image requirements section ([#118](https://github.com/seznam/jailoc/issues/118)) ([94bb6bc](https://github.com/seznam/jailoc/commit/94bb6bcd56a38c332acf0fd70fb3c0d02e25715b))

## [1.12.0](https://github.com/seznam/jailoc/compare/v1.11.0...v1.12.0) (2026-04-22)


### Features

* **cmd:** add restart command ([#83](https://github.com/seznam/jailoc/issues/83)) ([a8c46f2](https://github.com/seznam/jailoc/commit/a8c46f21d4fc863f00a0e5b50fb44bfc5ac68a7e))
* **config:** add expose_port option to disable host port exposure ([#109](https://github.com/seznam/jailoc/issues/109)) ([56c39b0](https://github.com/seznam/jailoc/commit/56c39b0add6c92f21ff6a459f37cba22df892dcd))
* **status:** show container resource consumption stats ([#110](https://github.com/seznam/jailoc/issues/110)) ([61ffaf6](https://github.com/seznam/jailoc/commit/61ffaf6d3849e0545c960e0d9e08d9fb02e3cb21))
* TUI sidebar plugin for workspace context ([#112](https://github.com/seznam/jailoc/issues/112)) ([3b9c1e2](https://github.com/seznam/jailoc/commit/3b9c1e271aecfcce88231c46ee275bc953c72708))
* **update:** add update-available notice via GitHub Releases API ([#108](https://github.com/seznam/jailoc/issues/108)) ([abca6d4](https://github.com/seznam/jailoc/commit/abca6d497a71ac066ccf3e5ade11183503128ec9))


### Bug Fixes

* **config:** reject reserved keys in env_file during validation ([#81](https://github.com/seznam/jailoc/issues/81)) ([112cb79](https://github.com/seznam/jailoc/commit/112cb79e2c7d8163b1ebd1d98ee81421143700a4))
* **password:** use Peek for migration detection to support all password modes ([#107](https://github.com/seznam/jailoc/issues/107)) ([1465b93](https://github.com/seznam/jailoc/commit/1465b938ae650fd38e93fe7518462ca9aa09dd51))
* **status:** add logs hint for unhealthy container states ([#111](https://github.com/seznam/jailoc/issues/111)) ([447f234](https://github.com/seznam/jailoc/commit/447f234b0e11625f57cc861246c0fae90d4c3d8a))
* **test:** use testWorkspaceDir in TestRestartLifecycle to avoid /tmp mount conflict ([3758b6c](https://github.com/seznam/jailoc/commit/3758b6c5e57a4988f083eee72756f6c4ebb75f6f))

## [1.11.0](https://github.com/seznam/jailoc/compare/v1.10.1...v1.11.0) (2026-04-19)


### Features

* **password:** automatic workspace password management ([#105](https://github.com/seznam/jailoc/issues/105)) ([903cd14](https://github.com/seznam/jailoc/commit/903cd148d0405753a1ce5743cf9a84f342ae09e5))

## [1.10.1](https://github.com/seznam/jailoc/compare/v1.10.0...v1.10.1) (2026-04-19)


### Bug Fixes

* **compose:** bind published port to localhost only ([6378b45](https://github.com/seznam/jailoc/commit/6378b45ffd448de1c7a4697bb8b78b6f78eaef2d))
* **test:** fix integration test build errors, /tmp paths, and timeouts ([#101](https://github.com/seznam/jailoc/issues/101)) ([64a3f44](https://github.com/seznam/jailoc/commit/64a3f442565e00980bf20461f505e05d8543dead))
* **test:** use docker/docker client and avoid /tmp workspace paths in integration tests ([#100](https://github.com/seznam/jailoc/issues/100)) ([bb85755](https://github.com/seznam/jailoc/commit/bb857552759409e1df493963d2f4002bb8c6e589))

## [1.10.0](https://github.com/seznam/jailoc/compare/v1.9.0...v1.10.0) (2026-04-17)


### Features

* add nix flake ([#63](https://github.com/seznam/jailoc/issues/63)) ([cf1eab3](https://github.com/seznam/jailoc/commit/cf1eab37badac4b3a01755421e6ef2e5d1e01b4e))
* **config:** add user-configurable mounts ([#68](https://github.com/seznam/jailoc/issues/68)) ([33f18b0](https://github.com/seznam/jailoc/commit/33f18b08e49f683f0662b96f9b7273e7f139f86a))


### Bug Fixes

* **embed:** fall back to iptables-legacy when nft backend is unsupported ([#74](https://github.com/seznam/jailoc/issues/74)) ([e403eb6](https://github.com/seznam/jailoc/commit/e403eb6e55d7fbdf52d4298a73a50b6a263b8072))
* **entrypoint:** avoid recursive chown on bind-mounted transcripts dir ([#69](https://github.com/seznam/jailoc/issues/69)) ([c5366d2](https://github.com/seznam/jailoc/commit/c5366d2af44303c25ca1014f7bce97f10004b389))
* **entrypoint:** suppress chown errors on read-only bind mounts ([#72](https://github.com/seznam/jailoc/issues/72)) ([a4536c4](https://github.com/seznam/jailoc/commit/a4536c4e90dbfadd600c3ca09c658f4b3fb60504))
* **up:** detect port conflicts before starting workspace ([#73](https://github.com/seznam/jailoc/issues/73)) ([9643d66](https://github.com/seznam/jailoc/commit/9643d66641adaa565fe22c950abb537f790b6d27))


### Documentation

* add pre-commit checks to AGENTS.md and development.md ([#82](https://github.com/seznam/jailoc/issues/82)) ([e947794](https://github.com/seznam/jailoc/commit/e94779445af8991ba251c6484b71a2f217fb321c))
* **how-to:** add auth passthrough and shared paths mount examples ([#85](https://github.com/seznam/jailoc/issues/85)) ([eed1228](https://github.com/seznam/jailoc/commit/eed1228c98b72e956a1a2a211fcddd7f70c2f45b))

## [1.9.0](https://github.com/seznam/jailoc/compare/v1.8.0...v1.9.0) (2026-04-09)


### Features

* automatic workspace selection by path ([#54](https://github.com/seznam/jailoc/issues/54)) ([f666df6](https://github.com/seznam/jailoc/commit/f666df62016d6f4a8a5bb5d564caf810bf0bd7ad))
* **config:** add SSH agent, git config, and known_hosts passthrough ([#52](https://github.com/seznam/jailoc/issues/52)) ([dc924b4](https://github.com/seznam/jailoc/commit/dc924b490816d7e84e49f09a0ef3ae8e79cc382c))
* configurable cpu and memory resource limits ([#58](https://github.com/seznam/jailoc/issues/58)) ([d401de4](https://github.com/seznam/jailoc/commit/d401de4d43cdf861fc35eee465ece4ad6abeb284))


### Bug Fixes

* **ci:** use existing draft release to prevent duplicate releases ([#53](https://github.com/seznam/jailoc/issues/53)) ([84ec66d](https://github.com/seznam/jailoc/commit/84ec66d5b72e7c8b07db0c9c14b72edddd246527))
* **podman:** whitelist DNS resolvers and collapse networks for Podman compatibility ([#57](https://github.com/seznam/jailoc/issues/57)) ([ad96de1](https://github.com/seznam/jailoc/commit/ad96de152bd83f1e5f3645c461fb260dfca8a6e8))

## [1.8.0](https://github.com/seznam/jailoc/compare/v1.7.1...v1.8.0) (2026-04-08)


### Features

* **cmd:** add colored terminal output ([#45](https://github.com/seznam/jailoc/issues/45)) ([26f3a75](https://github.com/seznam/jailoc/commit/26f3a752775cea22efdabe11d794846c917a32b8))
* **cmd:** pass --dir to opencode attach for subdirectory targeting ([#47](https://github.com/seznam/jailoc/issues/47)) ([c381d96](https://github.com/seznam/jailoc/commit/c381d9603961242903d8120bf8d6fcae3a36260b))


### Bug Fixes

* **ci:** append goreleaser assets to release-please release instead of creating separate draft ([e44c23c](https://github.com/seznam/jailoc/commit/e44c23c87bdcef67d5f4c491f7cfea9a8b3c834e))


### Code Refactoring

* **docker:** bind-mount entrypoint.sh at runtime instead of baking into image ([#50](https://github.com/seznam/jailoc/issues/50)) ([c6a3351](https://github.com/seznam/jailoc/commit/c6a3351cf710c9f015cbbdb00d4799952908dd5c))


### Documentation

* add llms.txt for LLM-friendly documentation discovery ([#46](https://github.com/seznam/jailoc/issues/46)) ([d41ca62](https://github.com/seznam/jailoc/commit/d41ca623bf83f894d5caa4c5191aa2f7c6dbe902))

## [1.7.1](https://github.com/seznam/jailoc/compare/v1.7.0...v1.7.1) (2026-03-27)


### Bug Fixes

* **docs:** align default container docs with actual Dockerfile ([#43](https://github.com/seznam/jailoc/issues/43)) ([70e8238](https://github.com/seznam/jailoc/commit/70e82381a181c72ef61ca7f9c8c18aa4caf486a3))
* **test:** skip Docker-dependent tests when daemon is unavailable ([#42](https://github.com/seznam/jailoc/issues/42)) ([47f3ccb](https://github.com/seznam/jailoc/commit/47f3ccb0cc3ccba3dbe309b1faa64fae7a8705c4))

## [1.7.0](https://github.com/seznam/jailoc/compare/v1.6.0...v1.7.0) (2026-03-27)


### Features

* **attach:** fall back to opencode-cli when opencode binary not found ([#38](https://github.com/seznam/jailoc/issues/38)) ([ca2adf9](https://github.com/seznam/jailoc/commit/ca2adf957bb6681c5f5dd8eb75aa982375db95ef))
* **docker:** add container healthcheck and attach health monitoring ([#39](https://github.com/seznam/jailoc/issues/39)) ([396e694](https://github.com/seznam/jailoc/commit/396e69488b3e5da3b385d84d77dc337907647901))


### Bug Fixes

* **ci:** update workflow triggers from master to main ([#32](https://github.com/seznam/jailoc/issues/32)) ([d10cc8d](https://github.com/seznam/jailoc/commit/d10cc8db089f6c4021095397334328d51693c4c2))
* **docs:** recurse into subdirectories when substituting version ([#29](https://github.com/seznam/jailoc/issues/29)) ([67a6e54](https://github.com/seznam/jailoc/commit/67a6e546e096bfc2d17107da8c4dfae96c81fff8))
* **docs:** recurse into subdirectories when substituting version ([#30](https://github.com/seznam/jailoc/issues/30)) ([4ba6f4c](https://github.com/seznam/jailoc/commit/4ba6f4c55aa4f0799302d858382fc40e008aadd0))
* narrow .claude mount to transcripts directory only ([#31](https://github.com/seznam/jailoc/issues/31)) ([b3fab09](https://github.com/seznam/jailoc/commit/b3fab092beff5f4c7e20230d446ed4f17b08bb96))
* **version:** use debug.ReadBuildInfo as fallback for go install ([#40](https://github.com/seznam/jailoc/issues/40)) ([e5cd73b](https://github.com/seznam/jailoc/commit/e5cd73bd19538c518119ff4e79b4500f0de48bd2))


### Documentation

* replace misleading ubuntu:22.04 defaults.image example ([#34](https://github.com/seznam/jailoc/issues/34)) ([fe50e92](https://github.com/seznam/jailoc/commit/fe50e92f77bbd9d9df3b4261004ff72a1bb4fe17))

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
