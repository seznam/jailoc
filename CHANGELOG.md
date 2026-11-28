# Changelog

## [2.11.0](https://github.com/seznam/jailoc/compare/v2.10.1...v2.11.0) (2026-11-28)


### Features

* **auth:** automatically open captured OAuth URLs in the host browser ([b879ecc](https://github.com/seznam/jailoc/commit/b879ecc2ea5054bc82951fa037e5ac50fc89dfad))
* **auth:** automatically open captured OAuth URLs on the host ([fbad317](https://github.com/seznam/jailoc/commit/fbad3177091fbda7b3a6736c13faf7129a248503))
* **brokerproc:** propagate upstream ca changed status notifications ([4a0ea40](https://github.com/seznam/jailoc/commit/4a0ea40aaf8592a52cf9dee96cdba0f592c80f47))
* **browser:** launch host OAuth URLs without killing foreground openers ([03253cf](https://github.com/seznam/jailoc/commit/03253cfcc3fd7e3a3ec5b8805de7ebbb8b0947d7))
* **cli:** add the manual self-update command ([95bc901](https://github.com/seznam/jailoc/commit/95bc901936e9f32a6ef1cc4062d9a1c8f9215a23))
* **cmd:** add explicit host authorization link handoff ([0b43f74](https://github.com/seznam/jailoc/commit/0b43f746101c2354de1abb138a364c20c8c8e578))
* **cmd:** integrate upstream proxy with up, reload, status and config ([9b1f521](https://github.com/seznam/jailoc/commit/9b1f521fb9dfd0421cacf497b2fb87aca93e8ac7))
* **cmd:** resolve workspace build inputs and detect read-write shares exposing them ([d879ec3](https://github.com/seznam/jailoc/commit/d879ec3916d43c75b730fee85806b453c4dedbf1))
* **cmd:** resolve workspace build inputs and detect read-write shares exposing them ([d4b7428](https://github.com/seznam/jailoc/commit/d4b74285ef1773b65b7c578029087ef0f3206686))
* **cmd:** route CA-enabled builds to builder VM and track reload state ([208999b](https://github.com/seznam/jailoc/commit/208999b6087080dbe92e556cf7d4f68fe069e217))
* **cmd:** wire broker upstream auth resolver and policy encoding ([29e18e0](https://github.com/seznam/jailoc/commit/29e18e08d9dfa364176e813fe096da26ad492ef8))
* **config:** parse and validate network.upstream_proxy ([1bf319d](https://github.com/seznam/jailoc/commit/1bf319def5ce0650aa8e73cc178f41451f583470))
* **docker:** add BuildCA snapshot and host fetch TLS root support ([9c19b1e](https://github.com/seznam/jailoc/commit/9c19b1e4cc8739c296afb3f96bab09e4d90fa7e1))
* **docker:** forward ca_bundle transparently into image builds ([e670e41](https://github.com/seznam/jailoc/commit/e670e4135d92c01d2ed932a8dd8c86f55e8932e4))
* **docker:** inject CA trust into Debian/Ubuntu Dockerfile stages ([32c5a00](https://github.com/seznam/jailoc/commit/32c5a00d589353d694f11ec65eb0d4a93e715047))
* **docker:** report whether a Dockerfile source is local ([6e992ea](https://github.com/seznam/jailoc/commit/6e992ea3fe09f1f39faa76e31b16c40cd5140047))
* **guest:** hand captured authorization URLs to the broker ([0ce0d0a](https://github.com/seznam/jailoc/commit/0ce0d0a3539861e8205224ba99f899220c8550cc))
* **netbroker:** add upstream proxy CONNECT dialer and helper lifecycle ([953bb12](https://github.com/seznam/jailoc/commit/953bb12111d075eba5e9d19defaf8cb14ac456b8))
* **netbroker:** chain egress through an upstream proxy ([6532cc0](https://github.com/seznam/jailoc/commit/6532cc04ef33184635adf7870ebd437b75bf9417))
* **netbroker:** expose pending OAuth links through a private socket ([610ee1e](https://github.com/seznam/jailoc/commit/610ee1e09c043914cb353a9585aea54923c9b551))
* **netbroker:** route allowed flows through upstream proxy and direct rules ([c3a7514](https://github.com/seznam/jailoc/commit/c3a751409f68d7c44da4a1167ccda74f25887ed8))
* **proxy:** apply proxy route changes on reload ([b9f8d99](https://github.com/seznam/jailoc/commit/b9f8d998860207be31119231a3b6b5f3ada7b024))
* **proxy:** automate host DNS, CA trust and listener broker setup ([0f633bc](https://github.com/seznam/jailoc/commit/0f633bc3b49076d8a6d58b259cf7dfb1a49c5e3e))
* **proxy:** automate host DNS, CA trust and listener broker setup ([6ba2ccf](https://github.com/seznam/jailoc/commit/6ba2ccf1294941a6d37d77b034bcf33075d27473))
* **update:** add manual self-update with release verification ([87ccec6](https://github.com/seznam/jailoc/commit/87ccec6c4492856138cd0ba2aa21cfe9a9004a59))
* **update:** apply a release cooldown to update checks ([cd4f047](https://github.com/seznam/jailoc/commit/cd4f047b4d9e6fd2341eb9b2df6fc9111c13e840))
* **update:** replace release executables atomically ([f26374b](https://github.com/seznam/jailoc/commit/f26374bd1072b71ae2cc79250048714572b59ee2))
* **update:** verify GitHub releases before installing ([2900851](https://github.com/seznam/jailoc/commit/2900851b0b81a3bcc8a2ffb4ddd79835ddecc209))
* **vm:** compose guest and builder CA bundles with upstream proxy CA ([5a1eb05](https://github.com/seznam/jailoc/commit/5a1eb059c581c05c011c8378cad43b640d330ad5))
* **vm:** trust builder CA bundle for image fetches and daemon ([804ee6e](https://github.com/seznam/jailoc/commit/804ee6e87751921447a5a57a32b4392ebc158dc3))
* **workspace:** resolve upstream_proxy and warn on unmatched direct targets ([9c22a58](https://github.com/seznam/jailoc/commit/9c22a584140fea0e9f7177c9af3b846568868712))


### Bug Fixes

* **agent:** declare Claude Code sandbox environment ([cb1e136](https://github.com/seznam/jailoc/commit/cb1e1364426a64e5671b379c63f33d5a4bcdaf7b))
* **auth:** preserve foreground browsers during recovery ([1fe03f6](https://github.com/seznam/jailoc/commit/1fe03f60473f0446bb99d8e5e9131c7b16685b30))
* **cmd:** detect build input aliases and replaceable share directories ([b46c4b1](https://github.com/seznam/jailoc/commit/b46c4b144925a7b4fe503990835e7670294ae6b8))
* **cmd:** gate CA builder routing on image builds ([e7a6f31](https://github.com/seznam/jailoc/commit/e7a6f310fb141a2714246d457d042b1e2c7cd326))
* **cmd:** include static upstream CA digest in builder VM spec ([44bf7c3](https://github.com/seznam/jailoc/commit/44bf7c3070a71b8b2f1f1b684c754bf984eca6b0))
* **cmd:** merge validated upstream proxy CA into managed builder BuildCA ([990cd09](https://github.com/seznam/jailoc/commit/990cd091c702b3aee607b0d064b6e7f3cf14920a))
* **cmd:** preserve build input paths and dangling symlink targets ([363ab13](https://github.com/seznam/jailoc/commit/363ab13bb296fb95dab10fff8fe2af9b1791e8b9))
* **cmd:** reuse one CA bundle snapshot across guest materialization and BuildCA ([9cfe320](https://github.com/seznam/jailoc/commit/9cfe320dffd7c18c9e0cde1b475322845eaceadd))
* **config:** reject dangling config symlinks during workspace share validation ([ebedd9d](https://github.com/seznam/jailoc/commit/ebedd9d8b30d3b1ab281038f84250caddef9e33e))
* **config:** reject invalid characters in upstream_proxy auth username ([c0717fe](https://github.com/seznam/jailoc/commit/c0717fe3ad36b649f8e114c26325ac4314fce358))
* **config:** validate workspace shares against loaded config path ([fcb5f08](https://github.com/seznam/jailoc/commit/fcb5f08b53eb1a98e2d50341949fd533a1222577))
* **docker:** allow forward stage references, preserve direct bundle roots and check secret size on build ([a2bed1e](https://github.com/seznam/jailoc/commit/a2bed1ef51d3a6097e6cad20e94e96fedc9597be))
* **docker:** clean fallback CA bundle, require dockerfile 1.3 and ignore unset ARGs ([44fb1e0](https://github.com/seznam/jailoc/commit/44fb1e05b99037809168c623e5cefe6a66fc8201))
* **docker:** expand ARG CA overrides against inherited ENV with ENV precedence ([2b25d30](https://github.com/seznam/jailoc/commit/2b25d3080858109ace9fff3b8f0f5289e000d22a))
* **docker:** preserve descendant ARG CA overrides and remove probe volumes ([43328e9](https://github.com/seznam/jailoc/commit/43328e9971cdf5b64b3e04d49724291c5c88d8b8))
* **docker:** supply initial CA defaults before late ARG declarations ([fbf740a](https://github.com/seznam/jailoc/commit/fbf740a96d3f8b8de957e439147a2ced651ff498))
* **guest:** redact escaped quotes in OAuth shell arguments ([cf90570](https://github.com/seznam/jailoc/commit/cf90570514e3e44587f5838dddccc61a4ef34c8f))
* **guest:** redact OAuth authorization queries from exec logs ([8080626](https://github.com/seznam/jailoc/commit/8080626a4ad43dc40399c7de3b3425c9533432db))
* **guest:** redact OAuth URLs before masking secret values ([3b8acec](https://github.com/seznam/jailoc/commit/3b8acec32cc9f8983a66c3d9e36f9a9dc31f7ef8))
* **netbroker:** exclude untrusted DNS associations from upstreamNames ([e2aa80e](https://github.com/seznam/jailoc/commit/e2aa80eb7156addd9fc8ae47789bb2b7bd2f4fc0))
* **netbroker:** open upstream CA nonblocking and recover blocked command routes ([b5e3776](https://github.com/seznam/jailoc/commit/b5e3776ebc07b96b6af5341a0cbae76bf69e14f9))
* **netbroker:** reject canceled requests before browser launch ([10882f8](https://github.com/seznam/jailoc/commit/10882f817c72e1f3ccc4156efd425aff596f4b46))
* **netbroker:** reject encoded controls in authorization hosts ([a8ea88c](https://github.com/seznam/jailoc/commit/a8ea88cafc3b7a2580a8b5b51af9a7c80bd96142))
* **proxy:** harden host setup ownership, recovery and CA trust tracking ([72d9afe](https://github.com/seznam/jailoc/commit/72d9afe6c9ef85dd1b0bc499debffb0332965332))
* **proxy:** probe listener broker without claiming the gateway slot ([8e4c696](https://github.com/seznam/jailoc/commit/8e4c696e9c25cc81572be039d82d79e6b5ded759))
* **proxy:** reject unsupported listener broker UIDs ([0cbbfa5](https://github.com/seznam/jailoc/commit/0cbbfa5d49624fed7fb515ac724b0222f67c4071))
* **proxy:** retain installed broker mode while unavailable ([5f38ebc](https://github.com/seznam/jailoc/commit/5f38ebcf77f7c93c0363c9abf0555ce3ab9d5632))
* **proxy:** serialize host setup and preserve restart and trust state ([3db16cd](https://github.com/seznam/jailoc/commit/3db16cdb6be9e9da95784d4019dd407ff9a1d3d9))
* **proxy:** validate host setup inputs and preserve existing trust ([790507b](https://github.com/seznam/jailoc/commit/790507bc7ae4d308fb1ed861486423489a93dce3))
* **proxy:** verify system CA and service ownership and harden CA rotation ([74e03a5](https://github.com/seznam/jailoc/commit/74e03a52bb5c6d4045cd888e2fe3081700fecf26))
* **update:** accept archives at the entry limit ([a5a0ece](https://github.com/seznam/jailoc/commit/a5a0eced8d3b95d539a2af8544e395ca5f8d3eba))
* **update:** prevent stale concurrent replacements ([16de62e](https://github.com/seznam/jailoc/commit/16de62e50ff57ccc79c12438d4442aa00517582f))


### Code Refactoring

* **guest:** share CA bundle environment variable list ([669b2bd](https://github.com/seznam/jailoc/commit/669b2bdcb6b848bddc62c30927b58c02178ff7ea))


### Documentation

* **agents:** explain Claude sandbox environment ([a852bda](https://github.com/seznam/jailoc/commit/a852bdad491c30ffc3498c5ed85a00be2f5e4ed8))
* **auth:** describe automatic browser opening and recovery ([44e9e9d](https://github.com/seznam/jailoc/commit/44e9e9d1448152ec777071ab62ef05de2a376b63))
* **blog:** explain workspace HTTPS setup in jailoc 2.11 ([e8579c0](https://github.com/seznam/jailoc/commit/e8579c09319a8aaab633912dcc0f391d0dab7a6d))
* **blog:** explain workspace HTTPS setup in jailoc 2.11 ([f676637](https://github.com/seznam/jailoc/commit/f676637e99ca5920039eca19eec40badfdf46bd3))
* **blog:** introduce the workspace proxy without upgrade framing ([698bafd](https://github.com/seznam/jailoc/commit/698bafd8762b78436baa3cfe3ff7a9e4c536b31d))
* document transparent ca_bundle forwarding for image builds ([fa77a67](https://github.com/seznam/jailoc/commit/fa77a67f10aa82090e940efebbd2ed89e75ce8e7))
* **home:** highlight agent and workflow choice ([e661d26](https://github.com/seznam/jailoc/commit/e661d26670c316b5824178ce357851cd7b62f50d))
* **network:** document upstream_proxy configuration and CLI output ([86546a5](https://github.com/seznam/jailoc/commit/86546a5a87772067b847c849de650386ccef96df))
* **positioning:** clarify harness and workflow neutrality ([41ed6d8](https://github.com/seznam/jailoc/commit/41ed6d8f8549d08ea85909c2a9289883fa3a0a16))
* **positioning:** clarify the agent and workflow boundary ([ef41ebd](https://github.com/seznam/jailoc/commit/ef41ebd05c0b6eed09ab60d9e540a5096ac75a77))
* **proxy:** include the workspace hostname in manual setup ([90530a4](https://github.com/seznam/jailoc/commit/90530a40c663f9a21fef17f88d13a3e42aa7e7f3))
* **security:** describe upstream proxy routing and trust boundary ([0eac3a8](https://github.com/seznam/jailoc/commit/0eac3a8e2582b57e369df757b6aacc20dcb2cdad))
* **security:** scope OAuth callback isolation guarantees ([b5b2ea1](https://github.com/seznam/jailoc/commit/b5b2ea1cfdb3ef504626e04c6915e7eb40abb0b2))
* **update:** describe self-update and its trust boundary ([f5addd6](https://github.com/seznam/jailoc/commit/f5addd61a77bbb6ed069b24d3ad4cb8e4f2f54ed))


### Tests

* **cmd:** keep missing OAuth socket paths short on macOS ([aaa0d7b](https://github.com/seznam/jailoc/commit/aaa0d7bfeeb303b2086db2f80d3bd36bf71bfb80))
* **docker:** fix composite literal and ARG line in CA override tests ([e536caf](https://github.com/seznam/jailoc/commit/e536caf6232effc5f56f88c615560e4a491aeb83))
* **docker:** fix FROM line in late-ARG integration test ([e83dd21](https://github.com/seznam/jailoc/commit/e83dd211dd0cbb46e9d21aaf0838de8f684ed417))
* **proxy:** resolve /tmp symlink on macOS in directory-chain test ([855d4a7](https://github.com/seznam/jailoc/commit/855d4a7331c6e40206092e5328375d5f3b2df43b))
* **vmintegration:** cover builder VM private-CA fetches and CA rotation ([8aefbea](https://github.com/seznam/jailoc/commit/8aefbea6bcc96ba5c21ee73a47c2722e2cac0ba1))

## [2.10.1](https://github.com/seznam/jailoc/compare/v2.10.0...v2.10.1) (2026-11-25)


### Bug Fixes

* **ci:** isolate locked docs builds from Pages deployment ([c8d3d51](https://github.com/seznam/jailoc/commit/c8d3d517afb8e5fcd38c2c130a1b48d3f333583e))
* **deps:** pin docs renderer with strict validation ([cea5442](https://github.com/seznam/jailoc/commit/cea54422e0f41dbeb8f9e94d92ccafe74d604e9e))
* **docs:** catch documentation build failures in CI ([9458877](https://github.com/seznam/jailoc/commit/9458877bb8be36e25a7a1939440e07be6840fe08))

## [2.10.0](https://github.com/seznam/jailoc/compare/v2.9.0...v2.10.0) (2026-11-25)


### Features

* **cmd:** add listener broker binary, gateway --listener-broker and host-proof ([93902cf](https://github.com/seznam/jailoc/commit/93902cff0e5755ce3db28efe56ef6ce2a0c1f2aa))
* **cmd:** layer desktop tools into workspace images ([efa31a4](https://github.com/seznam/jailoc/commit/efa31a41837af792088cd5b3b1d2b0eacf8376f0))
* **cmd:** wire desktop routes and foreground gateway ([1a6e906](https://github.com/seznam/jailoc/commit/1a6e9064c4230119790cf8bec5fe911880b7ecec))
* **config:** add desktop and web route configuration ([a10a957](https://github.com/seznam/jailoc/commit/a10a957b02472635da884ae13d55bd1ac4139e40))
* **docker:** add optional desktop image layer ([2990310](https://github.com/seznam/jailoc/commit/2990310a4515b5a9e32de0b21576d338779288c8))
* **gatewayhost:** add isolated host proof over the broker ([d1583ef](https://github.com/seznam/jailoc/commit/d1583ef2c67a07cd13861bddc23f80e0d686d3da))
* **gatewayhost:** add per-connection loopback listener broker ([07305b6](https://github.com/seznam/jailoc/commit/07305b6e74f5edd8f12796c511326612babb9c58))
* **gatewayhost:** plan owned hosts records with exact rollback ([c939f2a](https://github.com/seznam/jailoc/commit/c939f2a7b039e1eb25d5169aa632e50fd54bbe51))
* **guest:** open a terminal when the desktop starts ([072473d](https://github.com/seznam/jailoc/commit/072473d33e3ccbeafc0de3f3e906214aeed0e3ee))
* **guest:** start native desktop and allowlisted web relay ([4711532](https://github.com/seznam/jailoc/commit/471153252d8ecd0ef45d4f514ca947e7c4e3f6d0))
* **net:** forward OAuth loopback callbacks and guest 127.0.0.1 listeners ([7fd1d37](https://github.com/seznam/jailoc/commit/7fd1d374855dbc2fa1f36349bc079ead27bc2157))
* **proxy:** add HTTPS proxy for workspace routes ([211ecc7](https://github.com/seznam/jailoc/commit/211ecc7c94659d7c57a040582fa219718752abf9))
* **proxy:** limit the ingress CA to jailoc.local and always generate it ([d991d67](https://github.com/seznam/jailoc/commit/d991d6761d272ce610d0b245ca771d2535aa4592))
* **proxy:** rename web/gateway to proxy and make route auth opt-in ([0c084e8](https://github.com/seznam/jailoc/commit/0c084e80f4f348171c7ec4857acb94c948253d5c))
* **proxy:** run proxy in the background and auto-manage on up/down ([216580a](https://github.com/seznam/jailoc/commit/216580a46dc09e6fc53ffdc1761fa34adb162770))
* **proxy:** run the proxy in the background with an automatic jailoc.local CA ([26c1671](https://github.com/seznam/jailoc/commit/26c16713df197b545363281a81b7364d32337683))
* **proxy:** sign leaves with a constrained intermediate and keep the proxy CA in ~/.local/share/jailoc ([4b139da](https://github.com/seznam/jailoc/commit/4b139da9ceec7a4dfdaec12c7d2aa1e10225f5f0))
* **vm:** forward private web relay socket ([00ca5f9](https://github.com/seznam/jailoc/commit/00ca5f9611a9b11643884a10d2e64aca76ef2740))
* **vm:** host-side rotating and secret-redacting writer for console.log ([04adeeb](https://github.com/seznam/jailoc/commit/04adeeb7630e68185d244a4f4dd5c484cf2e5d83))
* **vm:** interpose rotating and secret-redacting writer for console.log ([9a993c2](https://github.com/seznam/jailoc/commit/9a993c2eb1a4c5476074af87be18dcf47dadf0fc))
* **vm:** move persistent agent state and ~/.cache onto sized state.img and cache.img disks ([7fa254a](https://github.com/seznam/jailoc/commit/7fa254a4b023cd08f1e818bd8706e21f9f7bac0d))
* **vm:** move persistent agent state and ~/.cache onto sized state.img and cache.img disks ([180696e](https://github.com/seznam/jailoc/commit/180696e43d7248faf85fc59e44d315a22cd1f6aa))
* **webgateway:** add authenticated HTTPS and WebSocket proxy ([f5cab08](https://github.com/seznam/jailoc/commit/f5cab08cf89d31381fc8919748f273675f8dcc3c))


### Bug Fixes

* **cmd:** detect tmpfs mount changes in restart and status checks ([936d3b5](https://github.com/seznam/jailoc/commit/936d3b5fab059faeaf243ea1db4dfb96ad6c0571))
* **cmd:** share VM instance context across gateway routes ([039fa14](https://github.com/seznam/jailoc/commit/039fa1475f1aad1d88402880cc1b90128b5188fe))
* **config:** reject protected host directories and symlinks to the root in workspace paths ([c195485](https://github.com/seznam/jailoc/commit/c195485c2d32a4b8866dda9fb9a7eaf9b1b66965))
* **config:** reject published web target overlaps ([b3eb6da](https://github.com/seznam/jailoc/commit/b3eb6da511f3389eddcd79dafafc9acbc5be4dfa))
* **gateway:** isolate route cookies, disable response caching and fix Linux CI ([d0d3bbf](https://github.com/seznam/jailoc/commit/d0d3bbf5e7820f3c18f1abcef978e83c112fc780))
* **guest:** bypass HTTP_PROXY when registering OAuth callback ([0912c55](https://github.com/seznam/jailoc/commit/0912c555e30eeddf34db895736fc85e0a666e30e))
* **guest:** mount tmpfs on OmO RPC directory for Unix sockets ([387ac55](https://github.com/seznam/jailoc/commit/387ac55e59645f501291d18541cbcd5bffc93129))
* **guest:** mount tmpfs on OmO RPC directory for Unix sockets ([21eb819](https://github.com/seznam/jailoc/commit/21eb8190ad01acc7e756df419f03883d5bb2d5de))
* **netbroker:** harden OAuth loopback callback proxy and registration ([cc53975](https://github.com/seznam/jailoc/commit/cc539756aceba460ff35f2f4249ef5707ed371be))
* **netbroker:** serialize OAuth registration with forwards and avoid NIC fallback ([63068ef](https://github.com/seznam/jailoc/commit/63068effe624970a526dea435cd657334ecc3214))
* **proxy:** address review feedback on proxy marshaling, headers and docs ([4c52d4c](https://github.com/seznam/jailoc/commit/4c52d4c40df68bcfa1634fbf55cb7423af8a4e36))
* **proxy:** close review races, prune on down and bind the proxy to one config ([a87a02d](https://github.com/seznam/jailoc/commit/a87a02d0a24848144793e03e310fc9f9a2d2a1a4))
* **proxy:** resolve relative workspace paths, root shares and prune port changes ([578e02a](https://github.com/seznam/jailoc/commit/578e02aa5a7965bc019daf5f5ee596a523bcc066))
* **vm,gateway:** unlock tmp disk flock on release and satisfy gosec ([75fc482](https://github.com/seznam/jailoc/commit/75fc482c6a441e7ac75dee78c9f5dd2d2f3c326a))
* **vm:** check reload shares against the running VM's disks and report full state and cache disks ([fb50025](https://github.com/seznam/jailoc/commit/fb5002572087fc92fd2d4f8eb15173559f9d6095))
* **vm:** fail secret delivery without a redactor and drain the console when the runner exits ([b5b7920](https://github.com/seznam/jailoc/commit/b5b792064b4d554e50264e9f8aa66bdde60aa8ff))
* **vm:** harden legacy home migration, ownership and version records for state disks ([33b35ca](https://github.com/seznam/jailoc/commit/33b35caae1704bd835cf091cef1848ee2814d185))
* **vm:** keep agent settings on ENOSPC, prepare custom agents' state and make the migration durable ([d0e5199](https://github.com/seznam/jailoc/commit/d0e5199f7b93aa59f554942e3dcd9b8614b644a7))
* **vm:** keep draining the console pipe on write errors and bound the stop drain ([a19b746](https://github.com/seznam/jailoc/commit/a19b746a0f707ef080f5315ff32dacdefb956d68))
* **vm:** read the console pipe in blocking mode so macOS sees EOF ([83365f1](https://github.com/seznam/jailoc/commit/83365f18f60ad6d022e4057c0c721c550a0068ce))
* **vm:** register console secrets before delivery and keep the console pipe open ([9149031](https://github.com/seznam/jailoc/commit/9149031a187dda4dce2c55230ba3f9cced6b096b))
* **webgateway:** block persistent service worker registration ([4ada5e6](https://github.com/seznam/jailoc/commit/4ada5e6aa778ccf825edce2e9670665cf57bfac7))
* **webgateway:** keep the login form origin with a same-origin referrer policy ([dba9c2c](https://github.com/seznam/jailoc/commit/dba9c2cb0fd5f611604031f1e89e011595b5ea2d))
* **webgateway:** resolve gateway lint findings ([744d8e3](https://github.com/seznam/jailoc/commit/744d8e37591cb924c6aa2aa680853f7044187ea3))


### Code Refactoring

* **gateway:** split native desktop into stacked PR ([1b5ede3](https://github.com/seznam/jailoc/commit/1b5ede304f408b7942811b4ec887b18106e4b51b))
* **proxy:** move host-proof and hosts planner into _test.go ([24eade4](https://github.com/seznam/jailoc/commit/24eade4404ed8411260aef6faf97ebaeed23ae88))
* **proxy:** unify internal naming around proxy and close truncated rights on darwin ([ff7e9ea](https://github.com/seznam/jailoc/commit/ff7e9ead5fd456636c87a06c478e6812036445cd))


### Documentation

* **desktop:** document desktop and HTTPS route setup ([f0f6556](https://github.com/seznam/jailoc/commit/f0f6556754dcd8215b4c1b003fc6e4ea15dc5374))
* **desktop:** document per-route __Host- cookie prefixing ([96ed281](https://github.com/seznam/jailoc/commit/96ed281f88abfc1763b80bf01fa000fa4da63bba))
* **desktop:** document the listener broker and host proof ([0c75eb3](https://github.com/seznam/jailoc/commit/0c75eb37f0a7f6b7ba57f5cb4bdf0d9f86f0bd64))
* explain that state and cache disks bound the host space of a workspace ([13ecec5](https://github.com/seznam/jailoc/commit/13ecec52258793385c5c1954f9b72f30b7f033ca))
* **security:** soften Seatbelt deprecation warnings to match upstream ([d157e17](https://github.com/seznam/jailoc/commit/d157e1721340773f6b526def2a03a08d23cd0c88))


### Tests

* **guest:** scope desktop PID reads to temporary root ([385e9e6](https://github.com/seznam/jailoc/commit/385e9e6f15ffea4788b2aaa6ef5d1ccfd10985ff))
* **netbroker:** avoid ephemeral port reuse across parallel tests ([0d85226](https://github.com/seznam/jailoc/commit/0d85226d68fe97687ab6c2e60f8d31a6aa7ed5c6))
* **proxyhost:** skip stray connections in broker command serve test ([b0312f6](https://github.com/seznam/jailoc/commit/b0312f604d4490835b64346412546bead03afe7a))
* **proxyhost:** skip stray connections in broker command serve test ([9714a5e](https://github.com/seznam/jailoc/commit/9714a5e174f43d7bc72439d7281705ccf17a700f))
* **vmintegration:** cover native desktop and web relay ([2863a50](https://github.com/seznam/jailoc/commit/2863a50b1fb8d545188bf049aedda9e0c55af07a))
* **webgateway:** document rejected TLS connection cleanup ([692c03e](https://github.com/seznam/jailoc/commit/692c03e50cec9ad099d410a6eaf4384e254702bf))

## [2.9.0](https://github.com/seznam/jailoc/compare/v2.8.1...v2.9.0) (2026-11-24)


### Features

* **network:** add agent_endpoints to replace the implicit agent rule hosts ([7832bde](https://github.com/seznam/jailoc/commit/7832bde7fe67c718da1040456022eb238f4d889b))
* **network:** keep built-in endpoints for single-provider agents ([9f78150](https://github.com/seznam/jailoc/commit/9f7815029e8b384b6a4c23e2de303288d339b4f5))
* **network:** require agent_endpoints for the agent rule ([d91c04e](https://github.com/seznam/jailoc/commit/d91c04e9c2aa6231bc1d0f1edd222800b68f7929))
* **network:** require agent_endpoints for the agent rule ([e7d29a2](https://github.com/seznam/jailoc/commit/e7d29a20e0f7f8b19f47c60e3c6579aead7dc1e2))
* **setup:** move setup and migrate wizards to huh prompts ([8957592](https://github.com/seznam/jailoc/commit/8957592e729b5414d40dc257166f10a0289bc49c))
* **setup:** move setup and migrate wizards to huh prompts ([b43b477](https://github.com/seznam/jailoc/commit/b43b4773b18a71511c96d384209f719728da73ef))
* **setup:** select model providers for agent_endpoints ([3854e31](https://github.com/seznam/jailoc/commit/3854e311b00ed61925ee91cd504b194ddcde8987))
* **vm:** confine the VM runner on macOS with a Seatbelt profile ([1afe6f0](https://github.com/seznam/jailoc/commit/1afe6f00f0c150c3a662dea76801ae0452b6debb))
* **vm:** confine the VM runner on macOS with a Seatbelt profile ([f3f1ae8](https://github.com/seznam/jailoc/commit/f3f1ae864865ae4d98ae8850298e7ccb383f2e24))


### Bug Fixes

* **agent:** disable opencode2 background service in read-only config ([2ab5bd0](https://github.com/seznam/jailoc/commit/2ab5bd0c087f6d5d42b472b0c30ee4cced9bc252))
* **agent:** disable opencode2 background service in read-only config ([5b2f07d](https://github.com/seznam/jailoc/commit/5b2f07dcfc63435403d8bb9104f2ee4bce75f0b2))
* **config:** clarify no-rules warning and test mixed-agent migration ([afda65c](https://github.com/seznam/jailoc/commit/afda65c8245c7c90a4c05106eb3d26420eabd8b2))
* **config:** keep restricted agent_endpoints when adding workspaces ([6c26fcb](https://github.com/seznam/jailoc/commit/6c26fcb050231a062cfdf7cf24f26d1ec62d4645))
* **config:** seed agent_endpoints for inherited agents ([b9aface](https://github.com/seznam/jailoc/commit/b9aface0e27079942f0a63a47fb69d53e57e2c68))
* **release:** guard homebrew tap against downgrades and normalize re-release config ([5f79c07](https://github.com/seznam/jailoc/commit/5f79c07f8e743e0c455e434239299d34288e4371))
* **release:** pin libkrun to a commit and GoReleaser to an exact version ([f68a27d](https://github.com/seznam/jailoc/commit/f68a27db5c7ab6a90d20eb954f0ea409fb109e05))
* **release:** publish releases and the cask only after attestation ([0b9da03](https://github.com/seznam/jailoc/commit/0b9da0301db9f2855fda69032ecc4af248d4b1d2))
* **release:** publish releases and the cask only after attestation ([1c44a06](https://github.com/seznam/jailoc/commit/1c44a06f2a4ae551a2d9972d41522d0e94dc8ab7))
* **release:** read isPrerelease before publishing the draft release ([2eb24a4](https://github.com/seznam/jailoc/commit/2eb24a45fa0101029ca5d2ec4dce872f4f29ec51))
* **release:** serialize per-tag release jobs and enforce draft/render-only GoReleaser ([98839c3](https://github.com/seznam/jailoc/commit/98839c3a19404b8702b29f59192f04a74b05ad40))
* **setup:** honour hidden steps, defaults and cancellation in accessible prompts ([dd776a2](https://github.com/seznam/jailoc/commit/dd776a22d4f1d87e1f7e392691638b3b51fe0135))
* **setup:** read stdin on demand, print descriptions in accessible prompts and escape review notes ([dc0633a](https://github.com/seznam/jailoc/commit/dc0633a0acc1cdd24180fe72d2b33941a9f18bd1))
* **vm:** grant resolved targets of lib_dir dylib symlinks in Seatbelt profile ([c0f9ead](https://github.com/seznam/jailoc/commit/c0f9eadc3bca28c10aa5e2917d224e1922c20672))
* **vmintegration:** expect gateway and guest-network reasons for host addresses ([0a37a31](https://github.com/seznam/jailoc/commit/0a37a31c972d3a232c8fdbbb85e0ca659c06ce26))
* **vmintegration:** require EACCES and per-protocol DNS denials ([d3f8ed1](https://github.com/seznam/jailoc/commit/d3f8ed1e644423f25d604c229adfe9f5384ead07))
* **vm:** reject vsock ports in VMM sandbox and verify no host vsock ([5811424](https://github.com/seznam/jailoc/commit/5811424fab7cd69f7c7e41269b33f6e557fa3675))
* **vm:** reject vsock ports in VMM sandbox and verify no host vsock ([b6c8e0b](https://github.com/seznam/jailoc/commit/b6c8e0bed72577ad7968d304e67599b38e3380e9))
* **vm:** resolve symlinks before dot-dot in Seatbelt path canonicalization ([80012d1](https://github.com/seznam/jailoc/commit/80012d13ffc109a52068422f709cd1876d9c54d8))
* **vm:** unlock tmp disk flock before closing file descriptor ([19c34eb](https://github.com/seznam/jailoc/commit/19c34ebc0b5ca52ee0fae28f33fdce14545ce17f))


### Documentation

* **setup:** add screenshots of the setup and migrate prompts ([ab7b88c](https://github.com/seznam/jailoc/commit/ab7b88c7c044756f7f5d6067262acf45cb9d778c))


### Tests

* **cmd:** avoid /var/folders temp HOME on macOS in config test ([e2bc395](https://github.com/seznam/jailoc/commit/e2bc39505302372c3d2b70f75c87c02097cdf16c))
* **cmd:** size huh form viewport so long macOS temp paths do not clip review title ([1bb096b](https://github.com/seznam/jailoc/commit/1bb096bbf177af1f19e4af39030cc7f7ba34ead6))
* **vmintegration:** dispatch vm-seatbelt shim on macOS ([6c231f9](https://github.com/seznam/jailoc/commit/6c231f958d8e5d8ad993c7c2654bff6045df129a))
* **vmintegration:** enable 1s tmp TTL only after fixture setup ([f52f8a3](https://github.com/seznam/jailoc/commit/f52f8a3aadb823d2c8da5ea2f5d3ea4fbfd7671e))
* **vmintegration:** prove negative egress and confined runner access end to end ([f882ef3](https://github.com/seznam/jailoc/commit/f882ef3c9752618f0164736b289dd5420e589648))
* **vmintegration:** prove negative egress and confined runner access end to end ([ed13c88](https://github.com/seznam/jailoc/commit/ed13c884e955e0252fe1702e44148bf25493a1bf))
* **vmintegration:** restore embedded jailoc-guest after building test binary ([d71c005](https://github.com/seznam/jailoc/commit/d71c005a5b6daba0b207808db6b92b51d5d74c8c))
* **vmintegration:** skip host addresses the guest also owns ([903fd84](https://github.com/seznam/jailoc/commit/903fd842b65277a03d8061da0950cdef27810b75))
* **vmintegration:** use 5s /tmp TTL sweep interval in vmintegration builds ([8661be1](https://github.com/seznam/jailoc/commit/8661be1c3c22dc6f245ec44d701a8b990f11d503))

## [2.8.1](https://github.com/seznam/jailoc/compare/v2.8.0...v2.8.1) (2026-11-23)


### Bug Fixes

* **config:** reject empty-query and https ubuntu_mirror URLs ([b66e98a](https://github.com/seznam/jailoc/commit/b66e98a26585dabd404e2fba3026642b5544bb8a))
* **image:** install Ubuntu packages from a configurable mirror, cz.archive.ubuntu.com by default ([a4e0f78](https://github.com/seznam/jailoc/commit/a4e0f7836d76445bf03eb39822fac9cdb4bee93a))
* **image:** install Ubuntu packages from a configurable mirror, cz.archive.ubuntu.com by default ([9a7d210](https://github.com/seznam/jailoc/commit/9a7d21077ad103900ae86fac72f9c0f50044fc18))

## [2.8.0](https://github.com/seznam/jailoc/compare/v2.7.2...v2.8.0) (2026-11-23)


### Features

* **cmd:** wire /tmp storage settings, live TTL reload and status reporting ([141ceda](https://github.com/seznam/jailoc/commit/141cedabfbc0549d80a8dc8324f0332e90a786e2))
* **config:** add shared builder disk sizing ([cdaabde](https://github.com/seznam/jailoc/commit/cdaabdeebf97a900411098059b1e5d6b71752440))
* **config:** add tmp_storage, tmp_size and tmp_ttl settings ([917f5df](https://github.com/seznam/jailoc/commit/917f5df781bf239321ff76759da3bddd8f8b01b4))
* **guest:** mount configurable /tmp, expire unused entries and report usage ([9296f74](https://github.com/seznam/jailoc/commit/9296f7474638749c4cbab739d75a85d995f2ee66))
* **image:** upgrade embedded base to Ubuntu 26.04 ([ff40cfd](https://github.com/seznam/jailoc/commit/ff40cfd59cca01c8f67bbc85c221bb04969a7cb0))
* **vm:** add configurable /tmp storage, capacity, TTL cleanup and usage status ([340e32e](https://github.com/seznam/jailoc/commit/340e32e419122ecdcc199c430d938eda3bdf7227))
* **vm:** attach private sparse scratch disk for disk-backed /tmp ([ae5d7e4](https://github.com/seznam/jailoc/commit/ae5d7e4efc6222081baacc140c611e46ec75b821))


### Bug Fixes

* **builder:** maintain private image builders under their lock ([132578b](https://github.com/seznam/jailoc/commit/132578be80baf6601b87fd24a7ad681173b9ade6))
* **builder:** manage storage and surface startup failures ([da4e15e](https://github.com/seznam/jailoc/commit/da4e15ecadcd980f730c02080504a3882c00aef4))
* **cmd:** classify current full console evidence before quoting ([49b15e1](https://github.com/seznam/jailoc/commit/49b15e181185d264f9e4ad11fe811e5924b51c9b))
* **cmd:** preserve main-screen terminal output when exiting shell or exec ([0d591ad](https://github.com/seznam/jailoc/commit/0d591ad05d73a7b8c936e0bd308372252a7cba3f))
* **cmd:** preserve main-screen terminal output when exiting shell or exec ([a674e24](https://github.com/seznam/jailoc/commit/a674e24e8ded374a93af70e0ef822123fdc32364))
* **cmd:** require data filesystem evidence for ENOSPC advice ([050f86c](https://github.com/seznam/jailoc/commit/050f86c048156bf5f148f833a4e118b4afd7b49b))
* **cmd:** surface bounded builder failure diagnostics ([7d76675](https://github.com/seznam/jailoc/commit/7d76675221da40760c3ce35565d50c5b4a7c38c5))
* **docker:** bound private builder cache maintenance ([7464584](https://github.com/seznam/jailoc/commit/74645842e7be77ae82082f3121e16335459877b4))
* **netbroker:** extend inspection leaf lifetime to 30d and track expiry by wall clock ([c023c77](https://github.com/seznam/jailoc/commit/c023c77f087e45feaa563dc744a8b8798c8931a1))
* **netbroker:** prevent inspection certificates expiring across host sleep ([a824ba0](https://github.com/seznam/jailoc/commit/a824ba010d322eeab05360cb0f78f9bc24c46890))
* **vm-runtime:** backport imago 0.2.6 to avoid truncating raw disks on discard ([0ce0266](https://github.com/seznam/jailoc/commit/0ce0266b695c1a23fc5cc7d5ccbd2c8570e1c8cf))
* **vm:** avoid EOF zeroing during builder disk formatting ([e6de8b8](https://github.com/seznam/jailoc/commit/e6de8b88e221d78fb9980d5a805c3338415c0c08))
* **vm:** cache resize dependencies before offline restarts ([758d540](https://github.com/seznam/jailoc/commit/758d54040a5ba120a4b4c799d1fe215886fd8614))
* **vm:** detect startup loss and discard stale console evidence ([3ecfd7e](https://github.com/seznam/jailoc/commit/3ecfd7e4387b6548b59f9eb338aa20d34b74fa6a))
* **vm:** resize builder disks before caching packages ([fcf31f8](https://github.com/seznam/jailoc/commit/fcf31f8cd3b591ee87b8471c5c5a106cc9ae26dc))
* **vm:** skip resize tooling when the data filesystem fits ([3f38545](https://github.com/seznam/jailoc/commit/3f38545bfb1193bb53aa80e7c669f904c6d265ba))


### Documentation

* **builder:** clarify conditional disk growth advice ([830a283](https://github.com/seznam/jailoc/commit/830a283c27b0d4775ca5c5ae4d27454959ac3a16))
* **builder:** explain conditional offline disk resizing ([10731a2](https://github.com/seznam/jailoc/commit/10731a2740d738bb81b71695f937485e5c0122a8))
* **builder:** explain latest filesystem evidence ([a1c1017](https://github.com/seznam/jailoc/commit/a1c10177bd49e08fae4f79f10fab05c37f4fac0d))
* **builder:** explain storage maintenance and recovery ([ec1ac48](https://github.com/seznam/jailoc/commit/ec1ac48ca9730254e2044e61cedb39b93d3aafcc))
* **skills:** add release-announcement skill ([72b4856](https://github.com/seznam/jailoc/commit/72b48561a31c7b7f2da10e9a0c7443e80f103df2))
* **skills:** add release-announcement skill ([02db8a8](https://github.com/seznam/jailoc/commit/02db8a871fc1cdf87c9a9346548fb4a5782a47c1))
* **tmp:** document /tmp storage, capacity, TTL cleanup and status ([1d6f5eb](https://github.com/seznam/jailoc/commit/1d6f5eb149ceb4acfb744afa99037d877cf34461))


### Tests

* **netbroker:** cover wall-clock cache expiry ([d19a4c7](https://github.com/seznam/jailoc/commit/d19a4c770e6b14033045aac887360290b125fcdf))
* **vm:** cover builder restart and guest disk growth ([2d5ca3c](https://github.com/seznam/jailoc/commit/2d5ca3cbb5a65a79234a68b2efae5e694e82778d))
* **vmintegration:** cover RAM and disk /tmp, TTL sweep and block discard ([3e4c43c](https://github.com/seznam/jailoc/commit/3e4c43cbfedd7e8d3d6c05b12156f3b8e22d8e7c))

## [2.7.2](https://github.com/seznam/jailoc/compare/v2.7.1...v2.7.2) (2026-11-19)


### Bug Fixes

* **cmd:** reject invalid workspace paths before VM startup ([5c67c70](https://github.com/seznam/jailoc/commit/5c67c70d6119f1b17f4ad11a868e9d6a581e971a))
* **cmd:** validate workspace paths before VM startup ([1baa787](https://github.com/seznam/jailoc/commit/1baa787b12255a47a40414a04d71f8a611d565f7))
* **deps:** update go-microvm to prevent SSH admission stalls ([9e887c6](https://github.com/seznam/jailoc/commit/9e887c61382c8d9b53d966696d2edef4733df2a8))
* **deps:** update go-microvm to prevent SSH admission stalls ([0c8ade0](https://github.com/seznam/jailoc/commit/0c8ade0ae4fdbc17e45a537c1aca45b98e065667))


### Documentation

* **config:** require existing workspace directories ([62274e3](https://github.com/seznam/jailoc/commit/62274e38ab6444161edad22e6ec779c196670915))

## [2.7.1](https://github.com/seznam/jailoc/compare/v2.7.0...v2.7.1) (2026-11-19)


### Features

* **migrate:** guide config upgrades and internal DNS trust ([844fb99](https://github.com/seznam/jailoc/commit/844fb99c4bc0acc9eec9a9f25df0ac37f696685e))
* **migrate:** offer internal zones for hostname allow rules ([6cfba18](https://github.com/seznam/jailoc/commit/6cfba180e98391b921f4d5e8b8fa0fc365dfba0f))


### Bug Fixes

* **agent:** allow overriding default session arguments ([e3c153f](https://github.com/seznam/jailoc/commit/e3c153ff42b97b9b5c1a001a4233e3f751ec5a5e))
* **agent:** allow overriding default session arguments ([086eb72](https://github.com/seznam/jailoc/commit/086eb725e381a630ad1f6fe3d92a8165db048c1d))
* **cmd:** recommend migration after deprecation notices ([2e40e10](https://github.com/seznam/jailoc/commit/2e40e10d854849a7ee1a19b74be1b9795de428f8))


### Documentation

* **migrate:** explain internal zone prompts and trust ([4e7bf80](https://github.com/seznam/jailoc/commit/4e7bf8026af9131d9e0cc4c49ad2aa52a2dbfa9d))

## [2.7.0](https://github.com/seznam/jailoc/compare/v2.6.0...v2.7.0) (2026-11-18)


### Features

* **cmd:** add bounded focus-aware notification transport ([b10a652](https://github.com/seznam/jailoc/commit/b10a6526ccf4ef7d3f2eb4d62f2e71efa28b195e))
* **cmd:** fail on missing read-only mount sources and warn on dangling share links ([ec23554](https://github.com/seznam/jailoc/commit/ec23554a81f2b335cdd42d86e03f61a9ccbd7342))
* **cmd:** fail on missing read-only mount sources, warn on dangling share links, disable Claude nonessential traffic ([bddc7a9](https://github.com/seznam/jailoc/commit/bddc7a95f1ad3549c1442d27237adaa78dcc6bef))
* **cmd:** notify from attached OpenCode sessions ([dfe6d04](https://github.com/seznam/jailoc/commit/dfe6d04ee7f2a0b01dc0f97b6d75ca2c9386f0c2))
* **cmd:** serialize terminal notifications without deferral ([d42e87d](https://github.com/seznam/jailoc/commit/d42e87d766dc852995eac2ead94621e2d728a258))
* **cmd:** show resolved notification delivery ([0b54cc5](https://github.com/seznam/jailoc/commit/0b54cc525880eed9ac175caab250c0d1e03069f3))
* **config:** add notification delivery settings ([b2ef831](https://github.com/seznam/jailoc/commit/b2ef8317d23b201c965dfc430d3cf3bcb1d76ad5))
* **guest:** keep tool download caches in the persistent cache ([616bc2a](https://github.com/seznam/jailoc/commit/616bc2aaf3380b8f5c47eb47ca8f7118cfb3eb80))
* **guest:** keep tool download caches in the persistent cache ([1f26d45](https://github.com/seznam/jailoc/commit/1f26d457c6623f1434f1f145e55ed31ceec1f11c))
* **netbroker:** answer flows denied after SNI/Host inspection with a TLS alert or HTTP 403 ([23e207c](https://github.com/seznam/jailoc/commit/23e207cefcead810897f8511fdb1e799fa009e86))
* **netbroker:** answer flows denied after SNI/Host inspection with a TLS alert or HTTP 403 ([ff4b867](https://github.com/seznam/jailoc/commit/ff4b867e70dfdf9fa443f8683c260a37d3d4c089))
* **netbroker:** inspect HTTPS and decide http rules per request by method and path ([d24598f](https://github.com/seznam/jailoc/commit/d24598f36fead09465c0cc281f7222b391b5bafa))
* **netbroker:** inspect HTTPS and decide http rules per request by method and path ([b1554c2](https://github.com/seznam/jailoc/commit/b1554c2c54d9e3952f472ff78ccf610566e80439))
* **network:** hide events of rules with log = false from jailoc traffic ([0715eb2](https://github.com/seznam/jailoc/commit/0715eb2534b93a017055409b9853ceba50fea058))
* **network:** hide events of rules with log = false from jailoc traffic ([420be74](https://github.com/seznam/jailoc/commit/420be7436495840a3762b43b466cd2adf69c23c5))
* **network:** resolve names to private addresses only in internal zones ([2c5875a](https://github.com/seznam/jailoc/commit/2c5875ad30324b37e3c58b9ffad2a02c17afa6e5))
* **network:** resolve names to private addresses only in internal zones ([527b833](https://github.com/seznam/jailoc/commit/527b8332fe2155d9cbc593b9d52f46f2911d5090))
* **notifications:** notify from attached OpenCode sessions ([63c3490](https://github.com/seznam/jailoc/commit/63c34908660a083f0aedb39794ef097bf53cda7e))
* **plugin:** define bounded private notification reports ([3d3355f](https://github.com/seznam/jailoc/commit/3d3355fe716a863db5c83f20f0f0212459afd3cf))
* **plugin:** report OpenCode 2 completion after child activity ([0631fe5](https://github.com/seznam/jailoc/commit/0631fe5f97f52a2b83da4bae9bca301ad7564aa6))
* **plugin:** report successful OpenCode 1 turns and input requests ([5dbf8d2](https://github.com/seznam/jailoc/commit/5dbf8d25984c8be9b5a92fb40fbaf12e6462d35f))
* **traffic:** collapse DNS answers into CIDR prefixes and octet patterns ([dd5d85f](https://github.com/seznam/jailoc/commit/dd5d85fbc32108c2d655181b995375fb663f7b27))
* **traffic:** collapse DNS answers into CIDR prefixes and octet patterns ([467a495](https://github.com/seznam/jailoc/commit/467a4952139fa9bbb3066ba2e5d21815366bad1e))
* **workspace:** resolve inherited notification delivery ([f8b0c20](https://github.com/seznam/jailoc/commit/f8b0c20c8fe4efe2d07ad94bb1515bf5cc0af292))


### Bug Fixes

* **agent:** let Claude Code start background sessions when switching sessions ([725fdcf](https://github.com/seznam/jailoc/commit/725fdcf2fc021765083262f726a6b64d288e28c9))
* **agent:** let Claude Code start background sessions when switching sessions ([7633a2d](https://github.com/seznam/jailoc/commit/7633a2d5d2456e846dc415c39243f578059dc96a))
* **cmd:** keep missing read-only config sources missing, count snapshots and running shares in the link check ([a7ae869](https://github.com/seznam/jailoc/commit/a7ae8697d80fba475322153e517779f9d8c8c27c))
* **cmd:** prepare config directories under existing read-only mount sources, quote paths in the dangling link warning ([a7fa9d1](https://github.com/seznam/jailoc/commit/a7fa9d11679d26b119101db92728b5d717453666))
* **cmd:** replace the agent state file through a temporary file ([04755f7](https://github.com/seznam/jailoc/commit/04755f779e062037716333664bfef41ae8a540a6))
* **cmd:** suggest snapshots for dangling file links, keep the harness settings in the dotfiles recipe ([e98cd23](https://github.com/seznam/jailoc/commit/e98cd2354c256965df4c25406808f92736b3537f))
* **docs:** clarify skipped Docker migration recovery ([1f9ba2f](https://github.com/seznam/jailoc/commit/1f9ba2feacdd745e9c7173d9a36d4dff2cad2c78))
* **guest:** honor npm cache environment aliases ([2ed9eac](https://github.com/seznam/jailoc/commit/2ed9eacd670eeb4d18c7bc0c513a29e32e6ee4fb))
* **images:** retain shared Docker daemon caches ([c15cd71](https://github.com/seznam/jailoc/commit/c15cd714d976cd50ea614d9705d672f2f777b1e9))
* **netbroker:** buffer small inspected request bodies so they are resent after an upstream GOAWAY ([3ce5f7a](https://github.com/seznam/jailoc/commit/3ce5f7a6c549dca2fa2e42498b9162e86eadc8b0))
* **netbroker:** check request paths only when a path pattern applies ([a657d84](https://github.com/seznam/jailoc/commit/a657d84eed6d213a3cb15a9c5cc50b6dff3bf0a8))
* **netbroker:** check request paths only when a path pattern applies, keep encoded slashes inside one segment ([2c2e768](https://github.com/seznam/jailoc/commit/2c2e768beaf47b515394996186da31c11f54eb4d))
* **netbroker:** close http rule bypasses on spliced, silent and no-ALPN flows, add cleartext h2c and IP-literal inspection ([7ddd9a0](https://github.com/seznam/jailoc/commit/7ddd9a0ec1c1ec3ad20dc19031596e82d47c8338))
* **netbroker:** deny whitespace-split and blank-line-prefixed request lines, classify buffered bytes at once, lock inspection CA creation ([83a17f2](https://github.com/seznam/jailoc/commit/83a17f2b91487f080df040edbe7cb9bc7f88b37d))
* **netbroker:** document that only HTTP/1.1 Expect bodies stream, test HTTP/2 Expect replay ([36ccc65](https://github.com/seznam/jailoc/commit/36ccc65e6bc2827c710d0e36b963efae28e4bddc))
* **netbroker:** drain a denied upload until the guest closes or the linger ends ([4b59399](https://github.com/seznam/jailoc/commit/4b59399f313a24bb37e4c5691c1f0f6411d155be))
* **netbroker:** inspect non-HTTP ALPN TLS covered by http rules ([575aab3](https://github.com/seznam/jailoc/commit/575aab3d68ef5038419a1f8a6c7e7937d22e5078))
* **netbroker:** inspect non-HTTP ALPN TLS covered by http rules ([c421315](https://github.com/seznam/jailoc/commit/c42131563a487c0a5f6e35e3a6d0ccc2de70f93a))
* **netbroker:** keep the product name out of the 403 body the guest sees ([0f65b5a](https://github.com/seznam/jailoc/commit/0f65b5a29b1870bec073ccf2759628f4978a40b2))
* **netbroker:** reject buffered bodies that run past their Content-Length, close the replaced test listener ([ca1c164](https://github.com/seznam/jailoc/commit/ca1c164899fc11fd59542c975a7e781f4ae3af71))
* **netbroker:** resend small inspected request bodies after an upstream GOAWAY ([331df43](https://github.com/seznam/jailoc/commit/331df43f9fa6b1dcd9f7a81d4e8454b6fdabb5f7))
* **netbroker:** reset malformed and pipelined denied requests instead of answering them ([8ad07a5](https://github.com/seznam/jailoc/commit/8ad07a53fa6451cb0ad18b3d45173250e0481c4c))
* **netbroker:** strip h2c upgrades, reject malformed request lines, canonicalize IPv6 hosts, skip inspection in isolated builds ([0ef0ffa](https://github.com/seznam/jailoc/commit/0ef0ffa1a8aa6f2db354d8d17531d4dfcd1c2442))
* **network:** keep untrusted cached names for deny rules and refresh their chain with the expiry ([83bd09c](https://github.com/seznam/jailoc/commit/83bd09c914a6b06093e89ff97edf897135d47963))
* **network:** require a broker restart for internal zones and close legacy and authority bypasses ([97f2416](https://github.com/seznam/jailoc/commit/97f24169fba2bf32f57bbe063ec96d623043de53))
* **network:** require internal CNAME chains, keep zone-only tables and warn about legacy aliases ([a42ba0a](https://github.com/seznam/jailoc/commit/a42ba0af2bc7ea1c732dbee09ac7326b29c23c2e))
* **network:** revalidate cached internal names, drop host-resolved legacy CIDRs, never cache error replies ([c66f014](https://github.com/seznam/jailoc/commit/c66f0145a519e954b1250abf4f4cbd6a432b0ac7))
* **notifications:** abort partial output before startup animation ([ffa2755](https://github.com/seznam/jailoc/commit/ffa2755aa83c6c22c653e2e1c19960fd390bd6b4))
* **notifications:** detect Linux native delivery from D-Bus ([ae250fe](https://github.com/seznam/jailoc/commit/ae250fe39422d937253771a1419159235aefc94b))
* **notifications:** honor OpenCode 2 legacy notification settings ([9d24c93](https://github.com/seznam/jailoc/commit/9d24c93621d2e8eabab24b1b7ca73a475f13bad5))
* **notifications:** match focus mode in long and padded lists ([08abd5a](https://github.com/seznam/jailoc/commit/08abd5a001254dd2ed092999ce5232c8c2d2db14))
* **notifications:** preserve buffered output when sessions end ([8efca4d](https://github.com/seznam/jailoc/commit/8efca4de27b8606fc92077690aa8409703881b10))
* **notifications:** preserve fresh requests after refocusing ([28185e1](https://github.com/seznam/jailoc/commit/28185e17d8d6d9129b28165ecad2a34e79dff61d))
* **notifications:** reassert focus reports after tmux passthrough ([7f857e3](https://github.com/seznam/jailoc/commit/7f857e364669eea848b84538cd7486316308959f))
* **release:** deploy docs after publishing assets ([d882293](https://github.com/seznam/jailoc/commit/d882293396e4d0db0b7398502137ef432c045c4c))
* **reload:** serialize workspace reload transactions ([eab0fe4](https://github.com/seznam/jailoc/commit/eab0fe45f1da8119d755c7643280ae8239494062))
* **session:** preserve redirected output during terminal cleanup ([79235c0](https://github.com/seznam/jailoc/commit/79235c0f82e966eb1640ca833749ff5aadf29e76))
* **validation:** address host testing findings ([caa8250](https://github.com/seznam/jailoc/commit/caa8250e97f4e06119126cd558069a38940b6c99))
* **vm-runtime:** prevent macOS FIFO unlink from stalling virtio-fs ([1cda825](https://github.com/seznam/jailoc/commit/1cda8250d823ade31cd7b60ae52ff06b30e40f2f))
* **vm-runtime:** prevent macOS FIFO unlink from stalling virtio-fs ([16fe949](https://github.com/seznam/jailoc/commit/16fe949199b0b6553c19d0e10542db4b1683efee))


### Code Refactoring

* **guest:** set the npm cache as NPM_CONFIG_CACHE ([d8c994d](https://github.com/seznam/jailoc/commit/d8c994d74159599e36c73831e0ae67ab0026302b))


### Documentation

* add the edit button and fix the network example rendered as a heading ([b14f2ec](https://github.com/seznam/jailoc/commit/b14f2ecbfbbab521cf93d46c1fc3792c3d325600))
* **agents:** clarify image and agent configuration choices ([d336aa8](https://github.com/seznam/jailoc/commit/d336aa82f230312cfadfdc49bec52a1d5742abba))
* **architecture:** make workspace lifecycle and resources easier to follow ([b1ee01b](https://github.com/seznam/jailoc/commit/b1ee01bae11cd66d7fa1210e4ad3a2422279e07d))
* **blog:** add jailoc 2.7 release post ([67fba63](https://github.com/seznam/jailoc/commit/67fba63cf8dc4ae69fb3f797f1ca9c3cc9099c56))
* **blog:** add jailoc 2.7 release post ([7beb26f](https://github.com/seznam/jailoc/commit/7beb26f56ae41e31d13fc7b5c940f00c462712b2))
* **blog:** register jailoc 2.7 post in docs navigation ([bfdaa1d](https://github.com/seznam/jailoc/commit/bfdaa1d6f89fb8ebc857046852b454ae2f1fd71c))
* **cli:** restructure command details for lookup ([28636fe](https://github.com/seznam/jailoc/commit/28636fed72ce6a33c9f034d61cbe211daa046801))
* **configuration:** separate workspace recipes from Git protection reference ([2c49812](https://github.com/seznam/jailoc/commit/2c498124a5395f70b07c4d53f005a997781d7995))
* **docs:** streamline user-facing documentation ([07fb32c](https://github.com/seznam/jailoc/commit/07fb32cad3b0087e201cbcb51ad98b1adf62f69d))
* **guides:** keep upgrade history in the migration guide ([8452716](https://github.com/seznam/jailoc/commit/84527163006ce14054de12ec909e5037c941b068))
* **home:** focus the documentation entrypoint ([16b2fe7](https://github.com/seznam/jailoc/commit/16b2fe79e62ded8a3d10a6ba0eba698a67fd1e1a))
* **images:** document manual Docker cache cleanup ([3d80971](https://github.com/seznam/jailoc/commit/3d80971d841665f1648d099d753f1ef4c64908fa))
* **images:** organize image resolution and compatibility reference ([b34abbb](https://github.com/seznam/jailoc/commit/b34abbb299860a2614fd2e1dab85893dcbf2ccf1))
* **installation:** add platform download actions ([52fb623](https://github.com/seznam/jailoc/commit/52fb623b5479bb46d9098726cd11533214d9eb3c))
* **installation:** template downloads from the release tag ([13c693d](https://github.com/seznam/jailoc/commit/13c693d2bf7c6d779cbc2016aefc5aa918a8de5b))
* **isolation:** structure network, secret and alternative explanations ([151f01c](https://github.com/seznam/jailoc/commit/151f01cd336bd5e1cc237ffaa2bfaa967191e964))
* **migrate:** consolidate upgrade guidance and recovery ([506ccec](https://github.com/seznam/jailoc/commit/506cceca29c6da75385f4bfe0858c30e585b7e6e))
* **migrate:** describe skipped hard-link names ([ca7e7c3](https://github.com/seznam/jailoc/commit/ca7e7c35bff1d8e62a5690401219f81b7bbdf0d9))
* **navigation:** use neutral upgrade links ([50aad4b](https://github.com/seznam/jailoc/commit/50aad4b057b9b95ce47f280f1bf4cf84548656f4))
* **network:** clarify HTTP name and address rule composition ([9b0d16f](https://github.com/seznam/jailoc/commit/9b0d16f74f6f139610d6f9203cad79e3896ecaf0))
* **network:** describe inspection of non-HTTP ALPN connections ([071b32e](https://github.com/seznam/jailoc/commit/071b32e424ced06fae622b894ceeb9e5dabb90b1))
* **notifications:** describe OpenCode 2 legacy config precedence ([e4b3e57](https://github.com/seznam/jailoc/commit/e4b3e578505af68db61bfa1c82b8620d4a6c48b4))
* **notifications:** explain attached-session delivery and trust limits ([dcb2f64](https://github.com/seznam/jailoc/commit/dcb2f643fcfad626c2356ee57ee84fdd7eb2084d))
* **onboarding:** put the first workspace run before internals ([8d00a18](https://github.com/seznam/jailoc/commit/8d00a18e8674a7e66a169a00cda6fef9c7e1266e))
* **operations:** organize network, session and troubleshooting tasks ([b6c894c](https://github.com/seznam/jailoc/commit/b6c894cb737b0b03a813adaf973ed62c4ffb1b24))
* **overview:** remove scattered legacy backend history ([c9ba381](https://github.com/seznam/jailoc/commit/c9ba3816851053b98357b4f5d2f2cafb834cd165))
* **readme:** sharpen the repository introduction ([c649ccd](https://github.com/seznam/jailoc/commit/c649ccd51ab0c3ffd7590aa4f69ebb9640cb21a8))
* **reference:** route legacy configuration details to migration ([9f6cbb0](https://github.com/seznam/jailoc/commit/9f6cbb04fdee232f9badf2a219bc36ed4e425676))
* **reload:** explain concurrent workspace reloads ([52d0752](https://github.com/seznam/jailoc/commit/52d0752f86f744d02cf1e6f4f04bdbad62e4cd96))
* **secrets:** put credential capabilities beside setup steps ([f007669](https://github.com/seznam/jailoc/commit/f0076698fc94be649a1dcfeee253c6452a2e541c))
* **security:** separate threat and boundary mechanisms for scanning ([42877ae](https://github.com/seznam/jailoc/commit/42877aece96d6ea6181d5d0fb15696a275520fa7))
* **setup:** make installation and migration steps easier to scan ([e7ddcad](https://github.com/seznam/jailoc/commit/e7ddcade6aa86a544283424907e89e9d6e793608))
* **skills:** add docs-editing skill ([104f72d](https://github.com/seznam/jailoc/commit/104f72d85a7244c8f2c764365cd75de7eefef6fd))
* **skills:** add task-first documentation editing workflow ([9324cf5](https://github.com/seznam/jailoc/commit/9324cf5bd5b7a371af3f491f3b21bb903b642026))
* **skills:** harden docs-editing scope, anchor and build checks ([9d29b06](https://github.com/seznam/jailoc/commit/9d29b069a710b973a59df42b7d10860892450ddc))
* **skills:** include the release manifest in scratch builds ([b849787](https://github.com/seznam/jailoc/commit/b8497873737ff00b3ec2f42eb215a99d4073c767))


### Tests

* **netbroker:** open a connection per rejected request, as Go closes it after a denied CONNECT ([5661c6f](https://github.com/seznam/jailoc/commit/5661c6f76d20aefe5c61301e1407ccccafc7120d))
* **plugin:** exercise shipped notification bundles in CI ([0aa1eb5](https://github.com/seznam/jailoc/commit/0aa1eb5a6546c0ed4be8a2d8421d5843f411705b))

## [2.6.0](https://github.com/seznam/jailoc/compare/v2.5.0...v2.6.0) (2026-11-16)


### Features

* **cmd:** show up timings in jailoc logs and warn about large images ([0157347](https://github.com/seznam/jailoc/commit/0157347ee7c41de1ca6126f320e1bb7b66df75a2))
* **cmd:** show up timings in jailoc logs and warn about large images ([f918e42](https://github.com/seznam/jailoc/commit/f918e42ebf4911b5834df2d5ab2367cb58f05899))
* **cmd:** warn only about what the workspace Dockerfile adds, above 20 layers or 5 GiB ([8748860](https://github.com/seznam/jailoc/commit/87488603703f9476ba775533c91426efe49950d8))
* **guest:** make more TLS clients trust a forwarded CA bundle ([bc1e15b](https://github.com/seznam/jailoc/commit/bc1e15bc86e5cca0fe57ba03a4a6fb94e1f1f2be))
* **guest:** make more TLS clients trust a forwarded CA bundle ([089d984](https://github.com/seznam/jailoc/commit/089d984eb8214f66b9b7628f7331b91283cc492a))
* **netbroker:** answer direct DNS to resolvers allowed by a port 53 rule ([0d54ae9](https://github.com/seznam/jailoc/commit/0d54ae9ebf08778e37c8760a09aa7dc08af88ef8))
* **netbroker:** answer direct DNS to resolvers allowed by a port 53 rule ([19f3afe](https://github.com/seznam/jailoc/commit/19f3afe6865e88459779c1f0d63f28f87c7f816c))
* **nix:** Jailoc v2 support ([b6ef6f5](https://github.com/seznam/jailoc/commit/b6ef6f54d7352b54c99f48eeefbad0ee866eabdd))
* **vm:** boot workspace VMs from per-layer EROFS images ([87fca57](https://github.com/seznam/jailoc/commit/87fca5770b5f26bddc3949b7cc45a22af3a7fdd4))
* **vm:** boot workspace VMs from per-layer EROFS images ([56acc84](https://github.com/seznam/jailoc/commit/56acc84a37d7d6dccb32ed8bf24b157ff98165bc))


### Bug Fixes

* **cmd:** follow the up, supervisor and console logs together and across restarts ([ebe1736](https://github.com/seznam/jailoc/commit/ebe1736ab8f98fa5a07b5cd793d61e797a4978ef))
* **cmd:** log up failures, time the export after the cache lock and follow a running up ([455e9b6](https://github.com/seznam/jailoc/commit/455e9b6b91a6c910d565f73dfeab0755c5ee47fc))
* **guest:** give the agent the host GID when idmapped mounts are unavailable ([5f84618](https://github.com/seznam/jailoc/commit/5f846189ccb910a996b5373ce7459c7b50a6e75d))
* **guest:** give the agent the host GID when idmapped mounts are unavailable ([9856147](https://github.com/seznam/jailoc/commit/98561478fa2c1ced15cda72890048c756ca87eba))
* **guest:** keep image owners without an idmapped boot root and mount deep images ([e1a2320](https://github.com/seznam/jailoc/commit/e1a2320a0c7a420838130fd48211e0f879c9c87a))
* **guest:** refresh generated trust stores when a forwarded CA is removed or replaced ([47d7f9b](https://github.com/seznam/jailoc/commit/47d7f9bc574a630e2734abe5e961dddc4fd4a646))
* **guest:** retry a failed trust store refresh and keep the system bundle current ([24cf519](https://github.com/seznam/jailoc/commit/24cf51931c56346538cfa7e5e80d8f6c5e6f394c))
* **guest:** run trust store tools from system dirs, tracked by the reaper, with content-named certificates ([fe5eb29](https://github.com/seznam/jailoc/commit/fe5eb29c2986da13ab01047163a204ace5894870))
* **netbroker:** share the DNS query and connection budget with direct DNS ([53eaed2](https://github.com/seznam/jailoc/commit/53eaed2f496be01ea9da6649eff06976b327bc92))
* **nix:** pin go-microvm runner to the EROFS share revision ([3eaa732](https://github.com/seznam/jailoc/commit/3eaa732687e719a471c720f9edb5343bd2a79d0f))
* **vm:** bound layer conversion and handle more tar and AUFS entries ([4a69795](https://github.com/seznam/jailoc/commit/4a69795d15fb633340e306278614b25516401996))
* **vm:** drop layers below a layer with an opaque root ([e3cdb23](https://github.com/seznam/jailoc/commit/e3cdb23b021f5fb2901c772ba7b5ebb6a3dec51f))
* **vm:** honour an opaque layer root in image path lookups ([44fb55a](https://github.com/seznam/jailoc/commit/44fb55adc191591aeb5ff1c7f10a05667c7f9ec8))
* **vmintegration:** allow the S3 host Docker Hub serves blobs from ([3fde769](https://github.com/seznam/jailoc/commit/3fde769612075891957e31e2e1a3e0351b1a30ca))
* **vm:** keep inherited directory metadata and resolve image paths across layers ([6ded59b](https://github.com/seznam/jailoc/commit/6ded59b6710633b7b22242c38951247cc64836b0))


### Documentation

* drop claim that sudo does not grant root without idmapped mounts ([78bd3e6](https://github.com/seznam/jailoc/commit/78bd3e66e4c218648b143689cf9168761853d0a4))

## [2.5.0](https://github.com/seznam/jailoc/compare/v2.4.0...v2.5.0) (2026-11-13)


### Features

* **setup:** explain wizard prompts and retain commented options in generated config ([5560a7f](https://github.com/seznam/jailoc/commit/5560a7fc4c80675a8e5634b5ebc6e5d7a9481085))
* **setup:** explain wizard prompts and retain commented options in generated config ([b3a001d](https://github.com/seznam/jailoc/commit/b3a001d9a0e6572f84bcb25a272adef53a7e3bba))


### Bug Fixes

* **agent:** enable storeTokenPlaintext for GitHub Copilot CLI and bump to 1.0.111 ([6a1c869](https://github.com/seznam/jailoc/commit/6a1c8698eae69cebef822bd37f8c9bd361f7678a))
* **agent:** enable storeTokenPlaintext for GitHub Copilot CLI and bump to 1.0.111 ([310f542](https://github.com/seznam/jailoc/commit/310f542a31b9074a0f137cdea50cf75c9f9ed882))
* **cmd:** bound GTK clipboard child initialization with signal.alarm ([6b99edb](https://github.com/seznam/jailoc/commit/6b99edb126c493798bdab9f828474c75d4dde724))
* **cmd:** clear alt-screen state on ESC c and preserve --config in exit hint ([8bb83cd](https://github.com/seznam/jailoc/commit/8bb83cd65cdab28fee3b9a05f72f2a344807fa85))
* **cmd:** isolate python3 GTK clipboard helper and keep selection owner alive ([b2ec7f1](https://github.com/seznam/jailoc/commit/b2ec7f189ab26aa954db32d10cf0a186f0946e29))
* **cmd:** lock and canonicalize agent state version history and purge on down ([6881321](https://github.com/seznam/jailoc/commit/6881321a6429848088c1dd46c8062cde45fe2f53))
* **cmd:** preserve main-screen agent crash output and surface startup diagnostics ([5b62761](https://github.com/seznam/jailoc/commit/5b627610aa178755f9a993ea8cca0e9a0c88a2b1))
* **cmd:** preserve main-screen agent crash output and surface startup diagnostics ([5f77b1d](https://github.com/seznam/jailoc/commit/5f77b1df63a9bcecf8485e5e67d6cff28ddd87ad))
* **cmd:** set both Linux clipboard selections and add GTK3 fallback ([a1c9c0f](https://github.com/seznam/jailoc/commit/a1c9c0f26bdce55416d58aff1843aa80ee6a1dc4))
* **cmd:** set both Linux clipboard selections and add GTK3 fallback ([41846c3](https://github.com/seznam/jailoc/commit/41846c38f0704341eeed87fbc79d3c5e3c480ba0))
* **cmd:** strip UTF-8 BOM in git config parser and reject hard-linked files in protect_git ([0ebf343](https://github.com/seznam/jailoc/commit/0ebf3433601bcfc89d88f036d7d1396255b6a1ae))
* **cmd:** synchronize GTK clipboard readiness and track owner-change count ([ed2c0e6](https://github.com/seznam/jailoc/commit/ed2c0e600ff20d62bd8f318f4b0783a60d411e7d))
* **cmd:** track agent state versions by host path after VM start and stop animator before terminal reset ([acb4df0](https://github.com/seznam/jailoc/commit/acb4df03d08f3ab08bdafb10ce6e90e3e3f5ce27))
* **config:** correct git_config template description ([908c464](https://github.com/seznam/jailoc/commit/908c464eca14a898c7373832e1934b23dbbc491a))
* **config:** preserve valid TOML when adding workspaces ([cd1e4e6](https://github.com/seznam/jailoc/commit/cd1e4e6e409344527751b3854c99b8f47b750a94))
* **docker:** confine remote Dockerfile redirects and block non-public dial targets ([141214f](https://github.com/seznam/jailoc/commit/141214fa394f3c1962a15789287e530b5533eae8))
* **docker:** use stdlib ProxyFromEnvironment to keep nix vendorHash unchanged ([bfca6d8](https://github.com/seznam/jailoc/commit/bfca6d8950c28a7ec4c1f60f17d983ec48f4c131))
* **netbroker:** reject HTTP obs-fold continuation lines in framing and policy inspection ([dbbf3bc](https://github.com/seznam/jailoc/commit/dbbf3bceee975302ce3b0022ac15e22fc2876472))
* **security:** harden HTTP framing, Dockerfile redirects, and protect_git checks ([5e071c0](https://github.com/seznam/jailoc/commit/5e071c0fe39e718c05c97fa67fecfeda146eaebc))
* **security:** pin proxied Dockerfile targets and reject bare CR/LF across HTTP buffers ([fb59418](https://github.com/seznam/jailoc/commit/fb59418195aeadfd1de752f65b024760ff87fb29))
* **security:** reject embedded CR/LF in HTTP headers and preserve proxy support in fetchDockerfile ([6bd20c7](https://github.com/seznam/jailoc/commit/6bd20c7bd85a75fb9dd5816c124ec0f3399527e3))
* **security:** separate proxy TLS ServerName, allow 5 redirects, and check external git config hard links ([f8c4204](https://github.com/seznam/jailoc/commit/f8c420466250a7d0a3d0c655e45f168ddefe45c0))


### Code Refactoring

* **setup:** tighten hints and explain memory reclaim ([bc35dd9](https://github.com/seznam/jailoc/commit/bc35dd9ae6b0b56a910ac1cfdb967929227ff274))


### Documentation

* **blog:** remove duplicate jailoc 2 nav entry ([b7a21e4](https://github.com/seznam/jailoc/commit/b7a21e4b2dba21d01849df94af4431c3b9a7bcea))
* **blog:** remove duplicate jailoc 2 nav entry ([b3335ab](https://github.com/seznam/jailoc/commit/b3335abad9f3c5e16f7e7d109643af3fe72fd50b))
* **homebrew:** clarify Landlock ABI requirement in cask caveats ([63b7154](https://github.com/seznam/jailoc/commit/63b71540c7cded6c41551385ee5e6abf686a1907))
* **homebrew:** clarify Landlock ABI requirement in cask caveats ([f434e2f](https://github.com/seznam/jailoc/commit/f434e2f7dd7e2c1e3e3afd07fa7fd5a24665fc1a))

## [2.4.0](https://github.com/seznam/jailoc/compare/v2.3.0...v2.4.0) (2026-11-12)


### Features

* **agent:** copy agent configuration read-only into the VM ([98ea73d](https://github.com/seznam/jailoc/commit/98ea73daded00e16b65eb4106eb611b2f6d73a0c))
* **agent:** copy agent configuration read-only into the VM ([a7178ad](https://github.com/seznam/jailoc/commit/a7178ad213d40e823ba1c9c4063f139f454378d8))
* **agent:** snapshot ~/.omo/omo.jsonc for opencode1 and omo ([975d327](https://github.com/seznam/jailoc/commit/975d32707420bdf907f15a20b411f7b6aa3ace47))
* **agent:** snapshot Claude and Copilot hooks and RTK.md ([e2e127b](https://github.com/seznam/jailoc/commit/e2e127b306a7c1404fdb26c8dc4bba08d6f72cea))
* **cmd:** add interactive setup/wizard command and first-run onboarding ([7600778](https://github.com/seznam/jailoc/commit/7600778e83adab99353fb1746145f1ce744a3684))
* **cmd:** add interactive setup/wizard command and first-run onboarding ([c13179a](https://github.com/seznam/jailoc/commit/c13179aad1628198a5eca4c505f97f471f9e884e))
* **cmd:** bridge OSC 52 clipboard sequences to the host clipboard ([ba4baf9](https://github.com/seznam/jailoc/commit/ba4baf92bfb7bab206d05283c57407757d3a4c85))
* **cmd:** display progress while unpacking workspace images ([8890a27](https://github.com/seznam/jailoc/commit/8890a270bb23e9de3bdf5a6ed1ddb218666da6f3))
* **cmd:** gate gh and glab secret prompts on host binary availability ([8a6011a](https://github.com/seznam/jailoc/commit/8a6011a7bc7f5e0423aa43fe74c05148d2d48c8e))
* **cmd:** mint per-workspace GitLab tokens over SSH in setup ([eb99715](https://github.com/seznam/jailoc/commit/eb9971573b07d5480563d91c2df41ef1f1e88ece))
* **cmd:** show image unpack progress and bridge OSC 52 clipboard ([5a4db49](https://github.com/seznam/jailoc/commit/5a4db4985ed6eaa185555cf11159702196cc8206))
* **cmd:** show resolved workspace config and computed paths in jailoc config ([67c92c0](https://github.com/seznam/jailoc/commit/67c92c017dac3cc83f74e7bfb177664298f80b7c))
* **cmd:** show resolved workspace config and computed paths in jailoc config ([aa19212](https://github.com/seznam/jailoc/commit/aa19212440774baa90c84ed8f69cc1891fbca53e))
* **homebrew:** install jl alias symlink alongside jailoc binary ([9ff92b6](https://github.com/seznam/jailoc/commit/9ff92b6b609ed52e7c9e930259e2e597ac0df3a7))
* **homebrew:** install jt alias symlink alongside jailoc binary ([579b62d](https://github.com/seznam/jailoc/commit/579b62d05f7d0bc99dce1e08d33f7cefd28ca893))
* **homebrew:** rename alias from jt to jl ([fb3611b](https://github.com/seznam/jailoc/commit/fb3611bf20436443759172a50aeb5fbb368626e9))
* **migrate:** add allow = ["*"] rule when migrating configs without network.rules ([328a0c4](https://github.com/seznam/jailoc/commit/328a0c4a32ade44440499f80c55abee097ff3a98))
* **network:** add default-deny egress rules and name enforcement ([f6f2515](https://github.com/seznam/jailoc/commit/f6f2515e1b8d8b2c8f832434620ffcbc75a43880))
* **network:** add default-deny egress rules and name enforcement ([be4cc92](https://github.com/seznam/jailoc/commit/be4cc921cb95e50a466b459e6ca814b7020a3396))
* **vm:** prune unused unpacked runtimes and guest binaries after 7d ([c106297](https://github.com/seznam/jailoc/commit/c106297c08ec25a09661ca0f71ba94b76b51d043))


### Bug Fixes

* **cmd:** address setup wizard review findings ([e754f17](https://github.com/seznam/jailoc/commit/e754f17c18c297027cc8526287bbd24071b7bcf1))
* **cmd:** close secret command stdout only on cancel and drop duplicate signal hook ([1576f5b](https://github.com/seznam/jailoc/commit/1576f5bcb8db42df68c36447e9f5b7b013c2d99d))
* **cmd:** compute layer prefix before extraction and drain clipboard queue on exit ([2e72bc7](https://github.com/seznam/jailoc/commit/2e72bc7869c14ab06e8862a65543cc04a933b685))
* **cmd:** keep lib_dir paths when pruning VM runtime cache ([1fb11b8](https://github.com/seznam/jailoc/commit/1fb11b8b7232946429416d83ea89b298f1764247))
* **cmd:** make secret commands interruptible and report slow runs ([891f4cb](https://github.com/seznam/jailoc/commit/891f4cb8767a6100cf05598c97d021b07b3a1174))
* **cmd:** make secret commands interruptible and report slow runs ([ace6b0e](https://github.com/seznam/jailoc/commit/ace6b0ec61585bebcee2d2aee040759b4193eb03))
* **cmd:** preserve OSC 52 selection and serialize layer progress callbacks ([349a1b3](https://github.com/seznam/jailoc/commit/349a1b319dbbbf63c93a8eea3e350c7c59eed2f7))
* **cmd:** report mounted agent state sources in computed profile dir ([cb98715](https://github.com/seznam/jailoc/commit/cb98715cf1536a52db0d27aa6839686563b6d8b7))
* **cmd:** resolve TUI config files individually and preserve snapshot removals ([aa056a7](https://github.com/seznam/jailoc/commit/aa056a759ca39583290ba792f9b6fbcbca3f7a56))
* **cmd:** resolve TUI plugins from snapshots and keep running shares on reload ([b7e3826](https://github.com/seznam/jailoc/commit/b7e382698154cbd7396cb7d8d9c9504bfe70056e))
* **cmd:** skip tmux passthrough for OSC 52 queries and adjust layer totals for reused prefixes ([9a74cc2](https://github.com/seznam/jailoc/commit/9a74cc2a96d7197030de097e299e64589ff89d97))
* **cmd:** use portable working directory parent in config test ([db9eecb](https://github.com/seznam/jailoc/commit/db9eecb2da3918e4f45fda172b4dcd952481b07f))
* **cmd:** warn on unknown config keys, accept JSONC TUI configs and name TUI plugin changes on reload ([2388a25](https://github.com/seznam/jailoc/commit/2388a25ec5a6496077e683c0bb146f5f8b71b42e))
* **cmd:** warn on unknown config keys, accept JSONC TUI configs and name TUI plugin changes on reload ([bb1f8a7](https://github.com/seznam/jailoc/commit/bb1f8a7c9dd9c846caee2cf9dd6e0d55df6e8411))
* **config:** explain why non-credential forbidden mount host paths are rejected ([ee681f5](https://github.com/seznam/jailoc/commit/ee681f51314b1565af96c5fbabd7db53a10ae682))
* **config:** explain why non-credential forbidden mount host paths are rejected ([4630b06](https://github.com/seznam/jailoc/commit/4630b063e722f75149979972cb1c09da7cfe8be8))
* **config:** preserve comments and formatting in AddPath ([1861009](https://github.com/seznam/jailoc/commit/1861009b704d1b4c073608393898b31e47382c97))
* **config:** preserve comments and formatting in AddPath ([2b9562c](https://github.com/seznam/jailoc/commit/2b9562c3a67039e748cdce14749fff6336c394d8))
* **guest:** exempt init from the OOM killer and log kernel OOM kills ([8a43127](https://github.com/seznam/jailoc/commit/8a4312783f04e98fb1358c3075da2abf5d76483d))
* **guest:** exempt init from the OOM killer and log kernel OOM kills ([e519801](https://github.com/seznam/jailoc/commit/e519801d9818cdb5bb4c598593bd14ff0dd54c6a))
* **guest:** keep session exemption restored by rescan on queued forks ([540a5b6](https://github.com/seznam/jailoc/commit/540a5b62d85f937c5ca2d7887156bbc32acc62e0))
* **guest:** reset inherited OOM values of orphans adopted by PID 1 ([33ce926](https://github.com/seznam/jailoc/commit/33ce92615cc52f819c2266c90b0854f123c03a67))
* **guest:** reset init's OOM exemption inherited by early forks ([ed0783d](https://github.com/seznam/jailoc/commit/ed0783d6f51927558f4b16d9dba78c05f1cd332e))
* **guest:** tell threads from processes in OOM guard fork events ([95ec7ab](https://github.com/seznam/jailoc/commit/95ec7aba0eebbd263e130ed3ce8d5c13400dcd9c))
* **migrate:** preserve blocked zone overrides and allow DNS-resolved private IPs for name rules ([3a17de4](https://github.com/seznam/jailoc/commit/3a17de4ad9f1511a60e8ce46b187cda76a026549))
* **migrate:** prompt on default blocked zone overrides and prefer CIDR allows ([d41eda4](https://github.com/seznam/jailoc/commit/d41eda41906c6e09d3fb70822195ac32c55f6ee1))
* **migrate:** rewrite config when only unknown keys need removing ([e6dfcd7](https://github.com/seznam/jailoc/commit/e6dfcd752b2cb38f2369e67eda8e7c14548baa25))
* **migrate:** rewrite deprecated network settings into network.dns and network.rules ([0d472d7](https://github.com/seznam/jailoc/commit/0d472d79cada2a8ff99dd1055d07c861f52db4e2))
* **migrate:** rewrite deprecated network settings into network.dns and network.rules ([bfb633f](https://github.com/seznam/jailoc/commit/bfb633f2a238cf373c3bd463595eb7fc802c8f03))
* **netbroker:** address CNAME TTLs, strict HTTP/TLS sniffing, and legacy rules ([a625e2e](https://github.com/seznam/jailoc/commit/a625e2ea5ae9bd8833633f1b7fc4db940d51f9fc))
* **netbroker:** allow unlisted CDN CNAMEs for allowed queries and reject HTTP Upgrade ([30fa3fc](https://github.com/seznam/jailoc/commit/30fa3fc103e52d52f788d409eac0e49884b070e8))
* **netbroker:** harden DNS cache, DNS filtering, and SNI/Host inspection ([efcbb9a](https://github.com/seznam/jailoc/commit/efcbb9a92882462ad2c0c0e11af66c6a93124b6c))
* **netbroker:** harden stream enforcement, mapped CIDRs, and broker reload check ([f6c3e19](https://github.com/seznam/jailoc/commit/f6c3e1914d52f9a0440610652ca588e654753f5c))
* **netbroker:** restrict legacy host CIDRs to private IPs and track HTTP body framing ([08afe40](https://github.com/seznam/jailoc/commit/08afe40dd23bbf52a0ecb5fc42efb05573d6f8b9))
* **setup:** harden broad-path check, option shortcuts, and CPU input ([f816751](https://github.com/seznam/jailoc/commit/f8167512780c77d09cc523b7aef717956bcaa3ba))
* **setup:** reload running VM and use onboarding directory ([c01ab9a](https://github.com/seznam/jailoc/commit/c01ab9ac30e744842999309663148d4ae9e0b8a7))
* **vm:** rename embedded runner binary to jailoc-vm-runner ([4fdb5e0](https://github.com/seznam/jailoc/commit/4fdb5e0613e97f9d492e9f29dfbd7eec88523b0e))
* **vm:** serialize runtime and guest cache access with pruning ([cc65623](https://github.com/seznam/jailoc/commit/cc65623ea1e2ddb2faf0abe4f26c98301ebd3756))
* **vm:** wake guest on control writes and shorten stop polls ([39bd4e2](https://github.com/seznam/jailoc/commit/39bd4e23d7ec0fdfa6f62c05f4ae90b73463ab65))
* **vm:** wake guest on control writes and shorten stop polls ([ab98a2b](https://github.com/seznam/jailoc/commit/ab98a2bd5e536aac7a34c7e6cce07c30c3537591))


### Code Refactoring

* **cmd:** drop gh and GitLab token prompts from setup ([6fa9457](https://github.com/seznam/jailoc/commit/6fa9457e29b38a7e46a28d636c1ef518d0f6f496))
* **config:** inline default dev-deps hosts and remove built-in [@dev](https://github.com/dev) list ([ffdd1d1](https://github.com/seznam/jailoc/commit/ffdd1d186edefe73162cca10e9ab91903357b67f))
* **config:** rename agent_config to import_agent_config ([987e914](https://github.com/seznam/jailoc/commit/987e914616ba198460bb97646775959326e966e5))


### Documentation

* **blog:** add network rules and setups post ([10c3b19](https://github.com/seznam/jailoc/commit/10c3b194c373598d53d0794dd293ab11d891be6b))
* **blog:** add read-only agent configs post ([c73d4b7](https://github.com/seznam/jailoc/commit/c73d4b772bb86e6f676f73bd3dc01203f1516540))
* **network:** clarify IPv6 link-local hard-deny and wildcard SNI inspection scope ([abe6f2b](https://github.com/seznam/jailoc/commit/abe6f2bc4c7fd5fcd203ec6725a1f254919fa9e7))
* **network:** document implicit agent endpoints and built-in broker rules ([099f78d](https://github.com/seznam/jailoc/commit/099f78d928eca80edaaf9597f7987a8ceb212e0a))
* **readme:** add downloads, homebrew cask, go version, and license badges ([ad0fc30](https://github.com/seznam/jailoc/commit/ad0fc30bb70bb7d4ca3a4c4573d56ce7e5a30a0a))
* **readme:** add downloads, homebrew cask, go version, and license badges ([d7b6c09](https://github.com/seznam/jailoc/commit/d7b6c09e55cc3acefabae0fac13c116fe683103a))


### Tests

* **setup:** place symlink outside /tmp in broad-path test ([7b4cf91](https://github.com/seznam/jailoc/commit/7b4cf913f9dbafe8d2c16cb1e7a066fc4c161df1))

## [2.3.0](https://github.com/seznam/jailoc/compare/v2.2.0...v2.3.0) (2026-11-10)


### Features

* **config:** reject credential directories as mounts and suggest file secrets ([a84bbed](https://github.com/seznam/jailoc/commit/a84bbed5927af7f07bce3b2655cf188ff90e9e74))
* **network:** make guest IPv6 opt-in via a [network] table ([6dbde1e](https://github.com/seznam/jailoc/commit/6dbde1ea256399224ed96a6f6fc4529b68a717aa))


### Bug Fixes

* **vm:** require restart only on guest protocol change ([d75b6fc](https://github.com/seznam/jailoc/commit/d75b6fc6bf281375057eb5d0bda44c2463aa0b85))
* **vm:** run workspace VMs in the host time zone ([f7dfd52](https://github.com/seznam/jailoc/commit/f7dfd52f8d10987f40cbbaefea764e57949eb388))


### Documentation

* **explanation:** compare agent-sandbox and note host-executed workspace files ([c6fe98f](https://github.com/seznam/jailoc/commit/c6fe98fdf2c7d99e7985710ff8101dba2fd87703))

## [2.2.0](https://github.com/seznam/jailoc/compare/v2.1.0...v2.2.0) (2026-11-07)


### Features

* **agent:** add Antigravity CLI (agy) harness ([8f09f63](https://github.com/seznam/jailoc/commit/8f09f6335c144920d5f765b4b7f209384f3d1fe6))
* **agent:** add Antigravity CLI (agy) harness ([8494d31](https://github.com/seznam/jailoc/commit/8494d311ef1e015a040726da7ff8e0970e25ad65)), closes [#314](https://github.com/seznam/jailoc/issues/314)
* **agent:** print the Copilot and agy status line without Node.js ([07b38d1](https://github.com/seznam/jailoc/commit/07b38d11f4504dffe00037d96f38743d5eae51d9))
* **cmd:** show agent startup status until the TUI draws ([427742a](https://github.com/seznam/jailoc/commit/427742a644db41aa22ca514690d76552cb22cc62))
* **cmd:** show OpenCode plugin installs in the startup status ([acf1ed9](https://github.com/seznam/jailoc/commit/acf1ed976e8e87942347d072ba9b385d20252f0c))
* **vm:** forward the host gpg agent into workspace VMs ([7182416](https://github.com/seznam/jailoc/commit/71824169d8dcc33295fc6c71ec33bbbf51daedb2))


### Bug Fixes

* **agent:** skip plugin hint on reattach, bound config reads and replace global plugin lists ([e16f025](https://github.com/seznam/jailoc/commit/e16f025fef07754f40242aa33cce9e0eea4c4873))
* **cmd:** resolve relative OpenCode config paths and guard narrow or piped status ([f1eaacb](https://github.com/seznam/jailoc/commit/f1eaacb951833d3e8fe1a3f579f1bbc04b39cf89))
* **cmd:** stop startup status on agent cursor saves and end only OSC on BEL ([ecb0559](https://github.com/seznam/jailoc/commit/ecb055945ae8b8766ff99074b72c3af813cf0875))
* **git:** copy all Git config files and their references into the VM ([9ebb6a6](https://github.com/seznam/jailoc/commit/9ebb6a664558672600e9ff76d784e0325cab80d5))


### Code Refactoring

* **agent:** parse OpenCode JSONC with tailscale/hujson ([c50eb77](https://github.com/seznam/jailoc/commit/c50eb77738f6f0000fcb783a85978b935046edd2))


### Documentation

* **explanation:** add comparison with alternative sandboxes ([e8ef61b](https://github.com/seznam/jailoc/commit/e8ef61beb7877fe53f7df9b62d8784a8244c67cf))
* **explanation:** add comparison with alternative sandboxes ([6e5f77e](https://github.com/seznam/jailoc/commit/6e5f77ee1263b8342670c275b248dc9c8851d39a))
* **explanation:** add license column to alternatives table ([7b791dc](https://github.com/seznam/jailoc/commit/7b791dc30d8a3eae0eff50805369ae575596c22c))
* **explanation:** mark comparison table with status emoji ([0574448](https://github.com/seznam/jailoc/commit/0574448a70e844a9a23a9222634037ed0dfb94e3))
* **explanation:** shorten alternative descriptions ([d1ad8c9](https://github.com/seznam/jailoc/commit/d1ad8c933f0b2b148e5301ea0f93878e1bc538c9))
* note that the status line indicator needs node on PATH ([81f86bc](https://github.com/seznam/jailoc/commit/81f86bc4e682da14648ff8544fc8e4776cc908a2))

## [2.1.0](https://github.com/seznam/jailoc/compare/v2.0.1...v2.1.0) (2026-11-06)


### Features

* **secrets:** add from_command ttl keychain cache and JAILOC_WORKSPACE ([ee5232e](https://github.com/seznam/jailoc/commit/ee5232e720e454f5baaa97062faa83bc7c5b60a4))
* **secrets:** add from_command ttl keychain cache and JAILOC_WORKSPACE ([feb7278](https://github.com/seznam/jailoc/commit/feb72788b56702d7cd242f01e80f557104050634))
* **vm:** log attributes and errors of go-microvm boot steps ([95e8ca6](https://github.com/seznam/jailoc/commit/95e8ca692f7f78f867510ca746522dc3abcef190))
* **vm:** log attributes and errors of go-microvm boot steps ([bda5f2b](https://github.com/seznam/jailoc/commit/bda5f2b0009174c15ca454d5445206ff4c90370c))
* **vm:** make the Docker data disk size configurable ([bb5068a](https://github.com/seznam/jailoc/commit/bb5068a58c993b2ed115c8178bb028ca601869a5))
* **vm:** make the Docker data disk size configurable ([c650cd4](https://github.com/seznam/jailoc/commit/c650cd4a7fd4f267eaf9234107655c5871ee22eb))


### Bug Fixes

* **agent:** upgrade pinned harness versions to latest in major line ([81ae742](https://github.com/seznam/jailoc/commit/81ae742d35c702bf1f6324403be26ccd5064c000))
* **agent:** upgrade pinned harness versions to latest in major line ([39a155a](https://github.com/seznam/jailoc/commit/39a155a23cc1eaba41dcf3134bf6fbbd7b414a0e))
* **deps:** resolve open Dependabot alerts ([b27576a](https://github.com/seznam/jailoc/commit/b27576a01d78b39048d7839e197cf178fc340e85))
* **secrets:** compute cache deadlines under lock and check keyring timeouts ([315e674](https://github.com/seznam/jailoc/commit/315e674e4b19f4f34dbcb832d38751613af76533))
* **secrets:** give nonempty cache index at least 24h TTL ([2a6032a](https://github.com/seznam/jailoc/commit/2a6032a15f493fb746ab40fe240a82b07c23ba74))
* **secrets:** give nonempty cache index at least 24h TTL ([866de7a](https://github.com/seznam/jailoc/commit/866de7afc2d612dc1bc7e000dc41a95c011622aa))
* **secrets:** grant user keyring permissions, keep expired retry index and honor context in lock ([4c83587](https://github.com/seznam/jailoc/commit/4c835873b017573bb453be49f3cd15c3e73e5c10))
* **secrets:** harden keychain cache write, read, validation and purge ([ba9cbdc](https://github.com/seznam/jailoc/commit/ba9cbdc02540d2dedb7df068420f40c1e47e67ea))
* **secrets:** lock cache index, keep longest expiry and report purge failures ([540e162](https://github.com/seznam/jailoc/commit/540e162417210b3d459d4488e480db0ef1c764a5))
* **secrets:** propagate purge generation read and write errors ([cfeed7a](https://github.com/seznam/jailoc/commit/cfeed7a16fe89cdbe1376eea63dd563bac5a9c59))
* **secrets:** run from_command outside cache lock with purge generation ([d69d35a](https://github.com/seznam/jailoc/commit/d69d35ab4edef3d64664d33840b4e9c7461d1827))
* **vm:** keep last value of repeated step attributes and use fake time in tracer test ([48417b0](https://github.com/seznam/jailoc/commit/48417b0d71426444812b4f76a11bbc386cebd6ce))
* **vm:** keep the guest clock on host time ([34d6466](https://github.com/seznam/jailoc/commit/34d6466d6d5031fa6ac4d1ab5d89cff0d7afa42c))
* **vm:** keep the guest clock on host time ([d4b2c87](https://github.com/seznam/jailoc/commit/d4b2c87f38eebd91ba9409a8771f5e03fd927072)), closes [#328](https://github.com/seznam/jailoc/issues/328)
* **vm:** log advisory step errors as warnings, not failures ([bfc2fda](https://github.com/seznam/jailoc/commit/bfc2fdae5131bfa126493ce04cb58664fc2c6758))
* **vm:** reject workspaces with more shares than libkrun has interrupts ([e5027d9](https://github.com/seznam/jailoc/commit/e5027d90b2ecd6ff445477df77d038b6fc3951e3))


### Code Refactoring

* **config:** rename disk to docker_disk ([904f9f5](https://github.com/seznam/jailoc/commit/904f9f51205e33561979ec47613b8c76558eb35f))

## [2.0.1](https://github.com/seznam/jailoc/compare/v2.0.0...v2.0.1) (2026-11-04)


### Bug Fixes

* **vm:** restore Linux KVM host fixes from [#283](https://github.com/seznam/jailoc/issues/283) missing in main ([448ebac](https://github.com/seznam/jailoc/commit/448ebacfc8017c8876d54836ed40df93bbf82854))

## [2.0.0](https://github.com/seznam/jailoc/compare/v2.0.0-rc.3...v2.0.0) (2026-11-04)


### Miscellaneous

* release 2.0.0 ([#305](https://github.com/seznam/jailoc/issues/305)) ([afc55eb](https://github.com/seznam/jailoc/commit/afc55eb95d8776d3c4ff971942735359b97d2e15))

## [2.0.0-rc.3](https://github.com/seznam/jailoc/compare/v2.0.0-rc.2...v2.0.0-rc.3) (2026-11-04)


### Features

* **git:** extend protect_git to mounts, worktree config, hooksPath and includes ([efdebf2](https://github.com/seznam/jailoc/commit/efdebf2f783011b446f8afef8dcd2ebd7baa8445))
* **guest:** redact secret values from the exec log ([1c8a74e](https://github.com/seznam/jailoc/commit/1c8a74e9bdc6f59120aab691aa0aebe0aace409d))
* **vm:** keep snapshots and config copies on a read-only internal share ([01b2016](https://github.com/seznam/jailoc/commit/01b201613b12538c2b0d43b2859b614b6b84ee45))
* **vm:** name the VM runner process after the workspace ([a1bda94](https://github.com/seznam/jailoc/commit/a1bda9441801d16cdf180564abfdc42759966e8d))


### Bug Fixes

* **cmd:** check nested shares against resolved share paths ([a4eb49a](https://github.com/seznam/jailoc/commit/a4eb49a12bf146f8b8fc74d7225bea1884dcc5b9))
* **cmd:** escape guest-provided text in session listings and errors ([0d18996](https://github.com/seznam/jailoc/commit/0d1899633b215ed5c92aca8f1d48cdb073fd0787))
* **cmd:** find the broker reload event across log rotation and show flow limits ([7d93536](https://github.com/seznam/jailoc/commit/7d9353610d76595fa585e08d349c21e73e4f7fcf))
* **cmd:** follow share paths link by link when checking nested shares ([86f87a5](https://github.com/seznam/jailoc/commit/86f87a52877419ed72a64acb38a2c197227c1511))
* **cmd:** keep following a log file until it is drained after rotation ([3776192](https://github.com/seznam/jailoc/commit/377619274cc8753a321548536a96e7c068f105fa))
* **cmd:** read guest-written files as bounded regular files ([7aff9f2](https://github.com/seznam/jailoc/commit/7aff9f202080048a52853860bd27a82721b274cf))
* **deps:** bump the go-minor-patch group across 1 directory with 3 updates ([#267](https://github.com/seznam/jailoc/issues/267)) ([cd13211](https://github.com/seznam/jailoc/commit/cd132113bc02d17b07ac8c60ba4a726f0473fd53))
* **git:** honour inherited hooksPath, read-only mounts and Git config value rules in protect_git ([d2b89ad](https://github.com/seznam/jailoc/commit/d2b89ada6775e56552f189a7c9ccddc8e19337f9))
* **git:** protect every hooksPath, guest view and worktree config separately ([8caff39](https://github.com/seznam/jailoc/commit/8caff39f67b284105a2a5363ac92053ae4f2b579))
* **git:** reject unreadable config files and links reached through share aliases ([410506d](https://github.com/seznam/jailoc/commit/410506da59d9cb854338707b22653833ed979a79))
* **guest:** detach snapshot mounts by descriptor on reload ([ac3ba6e](https://github.com/seznam/jailoc/commit/ac3ba6ed3bb2975e49a2075e910a78cd91c7e3f7))
* **guest:** keep redacting secret values replaced by a reload ([0291252](https://github.com/seznam/jailoc/commit/0291252bd4369a58e96ee26d7a45c61eb292627d))
* **migrate:** extract v1 volumes inside the destination root ([9725506](https://github.com/seznam/jailoc/commit/9725506436c8c319661f5e5004e86820d64aa8c6))
* **netbroker:** apply blocked DNS zones to answers and multi-question queries ([97a2a2d](https://github.com/seznam/jailoc/commit/97a2a2d555117385e28cbf6e552437a09588b7c2))
* **netbroker:** reopen the traffic log after a failed rotation ([fc41fc9](https://github.com/seznam/jailoc/commit/fc41fc9f6ba53cb8811d4b842bd5875723b0d7b9))
* **vm:** grant the VM process only the devices and files it uses ([01bd527](https://github.com/seznam/jailoc/commit/01bd527d39c1c38e6afbb47b02282a4a9f0fd129))
* **vm:** keep unpacked image files in the user's primary group ([ec522c9](https://github.com/seznam/jailoc/commit/ec522c9b1456c5102bf2cb9d61afbad715684737))


### Performance Improvements

* **netbroker:** cap concurrent flows and rotate the traffic log ([8f50a4f](https://github.com/seznam/jailoc/commit/8f50a4f9e44548cbc4a2c0741444d26da805e4d9))


### Documentation

* add SVG diagrams for startup, network policy, files, secrets, trifecta, memory and sessions ([fbc8354](https://github.com/seznam/jailoc/commit/fbc83546d5459056590078a0ee2ecdf0c59ba14d))
* describe protect_git coverage, internal shares, DNS filtering and broker limits ([804d600](https://github.com/seznam/jailoc/commit/804d600be661d83426662538b9d1a39fcf60ee54))
* draw the trust boundaries and image resolution as SVG diagrams ([8055e4c](https://github.com/seznam/jailoc/commit/8055e4cc57879ff5103e03b89ecc2342b1b90d25))
* explain VM memory, disk and host resource use ([8f654de](https://github.com/seznam/jailoc/commit/8f654dee7efc4d534f12887f49f49f770a9ec87a))
* replace the architecture flowchart with an SVG diagram ([5f7ccec](https://github.com/seznam/jailoc/commit/5f7ccec12b97bc09c21fa6b22ca08e8b0b3de41d))
* **security:** add security policy and security boundaries page ([4f5ead3](https://github.com/seznam/jailoc/commit/4f5ead344e8d568ad6b89920449fc08657965239))


### Tests

* **netbroker:** keep test socket paths within the macOS length limit ([e0acee4](https://github.com/seznam/jailoc/commit/e0acee4ca19531fec3a5277ed41aaafd455dca28))

## [2.0.0-rc.2](https://github.com/seznam/jailoc/compare/v2.0.0-rc.1...v2.0.0-rc.2) (2026-11-03)


### Features

* **release:** publish Homebrew cask to seznam/homebrew-tap ([4ad0e85](https://github.com/seznam/jailoc/commit/4ad0e852a9f7af8f94203812917ca8cebeb9e035))


### Bug Fixes

* **vm:** write spec atomically and hash build arguments in image cache key ([7e90d49](https://github.com/seznam/jailoc/commit/7e90d49ff715297cac968c8e18ad33be9dbf5706))


### Performance Improvements

* **docker:** skip builds of up-to-date embedded base and layer images ([83abada](https://github.com/seznam/jailoc/commit/83abada16d9a369eefff7378827476f77de4bcf1))
* **vm:** boot macOS workspaces through the guest overlay too ([099ffd5](https://github.com/seznam/jailoc/commit/099ffd5fc3741913f8cc870f0d19d673f4d25d9b))
* **vm:** boot workspaces from the shared image cache through a guest overlay ([e5028ce](https://github.com/seznam/jailoc/commit/e5028ce142f2561a9eaaa3162f35d823f932a570))
* **vm:** poll for the started guest every 50ms ([2b1990e](https://github.com/seznam/jailoc/commit/2b1990e00861200199dc0f204098d14df83af422))

## [2.0.0-rc.1](https://github.com/seznam/jailoc/compare/v2.0.0-rc...v2.0.0-rc.1) (2026-11-02)


### Features

* **release:** attest builds, pin the builder image and build macOS libkrun from source ([34c3a18](https://github.com/seznam/jailoc/commit/34c3a185017af6cd5187dfbea43c2f051c690350))
* **release:** publish SPDX SBOMs for archives and VM runtimes ([8b6aba5](https://github.com/seznam/jailoc/commit/8b6aba585edfb9681f31ce3affb1c5026c71e6ee))
* **strace:** show the exit status or signal of failed processes ([b59e331](https://github.com/seznam/jailoc/commit/b59e331646ddb8efd979766045bbb49efe2cb719))
* **vm:** embed the VM runtime in release builds and add linux/arm64 ([2f0f90f](https://github.com/seznam/jailoc/commit/2f0f90f5e9a4875a8fe3cef22858fc2d88f5afe2))
* **vm:** ship license notices and libkrunfw source with the embedded runtime ([907c5b9](https://github.com/seznam/jailoc/commit/907c5b9dacb451fc4a5492e0037f717e75ccd905))


### Bug Fixes

* **ci:** build the musl guest init for the VM integration libkrun ([40773f3](https://github.com/seznam/jailoc/commit/40773f3598d48704e790bd9ce75aba2a9a2ba838))
* **deps:** bump actions/setup-python ([d458f6e](https://github.com/seznam/jailoc/commit/d458f6e5533ffa0e5c06267ab852d0a44ddeb542))
* **release:** build the VM runtime, notices and source in the prerelease workflow and fail go-notices on go list errors ([4af5f18](https://github.com/seznam/jailoc/commit/4af5f18729f64b6987c199d2cd4e20e88bba92a5))
* **release:** give the krun-display build script a path to libclang on macOS ([3c1b367](https://github.com/seznam/jailoc/commit/3c1b367d8f0e9cfdd5cf6944ef5856171ba51c96))
* **release:** link libkrun's guest init against musl instead of glibc ([8e151c8](https://github.com/seznam/jailoc/commit/8e151c8e36f09fb336599274b5a5cb8986cf2560))
* **release:** point bindgen at the Xcode libclang when building macOS libkrun ([4a57aec](https://github.com/seznam/jailoc/commit/4a57aec715d706e84d8aed7c496a6050b688d832))
* **release:** restore the executable bit on the VM runtime build scripts ([927ce36](https://github.com/seznam/jailoc/commit/927ce36d87913f9b87bebe88e887eff695d6dda7))
* **session:** keep a detaching client from re-creating an ended session entry ([fdc78b8](https://github.com/seznam/jailoc/commit/fdc78b8e237af23ccf9d8f18e9c2fe05c5005a72))
* **strace:** report a process exit when its last thread exits ([1d0a9fe](https://github.com/seznam/jailoc/commit/1d0a9fea0be65ae978d6be968570bf732cffd65d))
* **strace:** stop tracking processes after dropped proc events ([a2a7b17](https://github.com/seznam/jailoc/commit/a2a7b17b380ff6f08a18f47c2f754f3744312a89))
* **vm:** allow the private image cache directory mode in gosec ([7b1a7a6](https://github.com/seznam/jailoc/commit/7b1a7a624088092bfbfd11dadd22c87f34bd1fe3))
* **vm:** keep the image cache private for set-id rootfs trees ([d270be3](https://github.com/seznam/jailoc/commit/d270be39aadad130c8618e8939df39378b32e278))


### Documentation

* **blog:** add jailoc 2 release blog post ([7ae25b1](https://github.com/seznam/jailoc/commit/7ae25b130eef05a1383d8905362cbccd8f977d34))
* **blog:** add traffic, strace, sessions and shell screenshots to the v2 post ([f58fa23](https://github.com/seznam/jailoc/commit/f58fa23b776a59e9f467aac94b5a3d62b97dd519))
* **blog:** fix v1 breaking changes in jailoc 2 post ([dc952c5](https://github.com/seznam/jailoc/commit/dc952c5cd83a3cc4e3ad5b56a146188b7d47f28b))
* **blog:** say the VM runtime is embedded, not downloaded ([ff5b8c2](https://github.com/seznam/jailoc/commit/ff5b8c22b325867f7d1c61ffdba11de0d644267a))

## [2.0.0-rc](https://github.com/seznam/jailoc/compare/v1.19.0...v2.0.0-rc) (2026-11-02)


### ⚠ BREAKING CHANGES

* **config:** enable_docker defaults to false. Set enable_docker = true in [defaults] or a workspace to keep Docker inside the VM.
* **image:** the embedded base image no longer contains Docker or nftables. Workspace overlays that run the docker CLI during the build need enable_docker on; a [base] dockerfile without nft on PATH needs apt-get for the Docker layer.
* **agent:** OpenCode is no longer part of the embedded base image; jailoc installs it in a separate layer. A `[base] dockerfile` needs npm on PATH, and an image used directly (`image`, or `defaults.image` without a workspace `dockerfile`) must contain the agent binary. The default mount of ~/.claude/transcripts is removed.
* the Docker backend and remote mode are gone, copies is renamed to snapshots, and env secrets are no longer written to /run/secrets.

### Features

* **agent:** add OpenCode 2 harness and workspace indicator for every agent ([d2baf9e](https://github.com/seznam/jailoc/commit/d2baf9e09b0d2896c9503b639c229f3b9380868e))
* **agent:** add per-agent mounts and state profiles ([5e7d2dc](https://github.com/seznam/jailoc/commit/5e7d2dcb3e1204805829158f610808399d66c9f9))
* **agent:** derive custom agents and override agent versions ([896d7b7](https://github.com/seznam/jailoc/commit/896d7b7d1b72191eeb86cc4c02405bed8d14cfa2))
* **agent:** mount ~/.omo read-write for opencode1 ([8fe8d00](https://github.com/seznam/jailoc/commit/8fe8d00feb0e5c29ed578f6a893b94909fd4ec82))
* **agent:** run Claude Code, Copilot CLI and OmO besides OpenCode ([cba7c65](https://github.com/seznam/jailoc/commit/cba7c65b92508f51082efb04de7b1bdd2777d82a))
* **cmd:** add --all to up, down, restart, reload and status ([c0e46a8](https://github.com/seznam/jailoc/commit/c0e46a8069a282daf728bf398ace2e0465682f91))
* **cmd:** add jailoc list and show agent, resources, sessions and drift in status ([72a28cd](https://github.com/seznam/jailoc/commit/72a28cd072e83963d552b0eb96f8aa97b74ab17e))
* **cmd:** show host_ports connections and their target in jailoc traffic ([b90f2f7](https://github.com/seznam/jailoc/commit/b90f2f7fc8553a5e014cc476987959c9399b795c))
* **config:** add --config flag and JAILOC_CONFIG to use another config file ([d46ac59](https://github.com/seznam/jailoc/commit/d46ac59cf8db97a86521dc2c915ef3eec56c088a))
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
* **netbroker:** relay host loopback ports into workspaces with host_ports ([cd61c04](https://github.com/seznam/jailoc/commit/cd61c0444ecaba7b6c714d3a9c4a33051d3d0d60))
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

* **cmd:** address PR feedback on bulk operations and install gh in dev image ([36150f9](https://github.com/seznam/jailoc/commit/36150f986bbe87ea54ace2a1ac3be5abe8eaea65))
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
* **guest:** propagate half-close in loopback relays ([5d38be1](https://github.com/seznam/jailoc/commit/5d38be1088a98b7e331eb181640ec1ea216da8da))
* **guest:** share one session between concurrent opens in a directory ([300ea38](https://github.com/seznam/jailoc/commit/300ea38f9e99d5da962672a5c9072aeae940291c))
* **netbroker:** close connections to a replaced host_ports target on reload ([af6b5e5](https://github.com/seznam/jailoc/commit/af6b5e5a5c0a65a00970f0335c99fec1dbeb4a95))
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
* **config:** document the opencode1 ~/.omo mount ([4887a7d](https://github.com/seznam/jailoc/commit/4887a7db5d2c4a2951c7925536d55f65811e76bf))
* describe detach keys in extended keyboard modes ([c0189ef](https://github.com/seznam/jailoc/commit/c0189efb874635c89a8ac3267b3f3d70a9bad9a5))
* describe jailoc v2 as VM-only ([eb08f4d](https://github.com/seznam/jailoc/commit/eb08f4ddfa7df7cfb171bb2228fbea77c8d2633d))
* describe workspace boundary fixes ([9f6d5b9](https://github.com/seznam/jailoc/commit/9f6d5b9acf03ad4a64d48f63c60a3200d2749053))
* **dev:** note enable_docker for the Docker integration tests ([0a3d8c9](https://github.com/seznam/jailoc/commit/0a3d8c9a6b037d629ba8f27aa856ae664ca66271))
* tighten Diátaxis structure, drop v1 mentions, warn about shared profiles ([3521a27](https://github.com/seznam/jailoc/commit/3521a279554953dc6491f9267544d725018d7484))
* **vm:** list VM backend restrictions and image cache pruning ([19f48ff](https://github.com/seznam/jailoc/commit/19f48ffcc6612a36de34b4ef60b4e667537af6a5))


### Tests

* **config:** clear JAILOC_CONFIG so tests never write the real config ([a789b32](https://github.com/seznam/jailoc/commit/a789b32115e76b902ef3175d2e95270c15b39d54))
* **session:** wait for the client to attach before stopping the session ([241fb11](https://github.com/seznam/jailoc/commit/241fb118bfb7bcffa823b2a1090ce506852ba2fd))
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
