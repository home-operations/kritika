# Changelog

## [0.0.53](https://github.com/home-operations/kritika/compare/0.0.52...0.0.53) (2026-10-09)


### Bug Fixes

* **agent:** keep a large tool result in the conversation, since dropping it breaks the prompt cache ([#827](https://github.com/home-operations/kritika/issues/827)) ([b2f643d](https://github.com/home-operations/kritika/commit/b2f643d777e0f65c9c2ec7d0e7e8f60d70c95ea4))
* **worker:** wake a review snoozed for a model slot when one freed while the snooze was saved ([#824](https://github.com/home-operations/kritika/issues/824)) ([c33b31c](https://github.com/home-operations/kritika/commit/c33b31c4e95ba38f1d2762e9222084205a60fbd6))

## [0.0.52](https://github.com/home-operations/kritika/compare/0.0.51...0.0.52) (2026-10-09)


### Features

* **go:** update module golang.org/x/sync (v0.23.0 → v0.24.0) ([#822](https://github.com/home-operations/kritika/issues/822)) ([6018ccc](https://github.com/home-operations/kritika/commit/6018ccc669f8a95312a2214fd0d4faffcca14ea6))
* **review:** give a review a skill its scope loads in the system prompt ([#823](https://github.com/home-operations/kritika/issues/823)) ([2c81737](https://github.com/home-operations/kritika/commit/2c817375e0b38680a393fd7d1e35980aec18d458))


### Bug Fixes

* **agent:** drop a large tool result from the conversation once it has been read ([#821](https://github.com/home-operations/kritika/issues/821)) ([561b4db](https://github.com/home-operations/kritika/commit/561b4db24b9dbab084c500d1c04bf00374f8644b))
* **container:** update image docker.io/jdxcode/mise (2026.10.5 → 2026.10.6) ([#813](https://github.com/home-operations/kritika/issues/813)) ([2a293e5](https://github.com/home-operations/kritika/commit/2a293e5ff9e07f5da7b26f3d2c9af244c6b1697f))
* **npm:** update dependency svelte (5.57.1 → 5.57.2) ([#818](https://github.com/home-operations/kritika/issues/818)) ([1e2499b](https://github.com/home-operations/kritika/commit/1e2499b12dd857acdb837cf487b57fabc6ed8a25))
* **worker:** wake a review snoozed for a model slot when one frees ([#819](https://github.com/home-operations/kritika/issues/819)) ([069e7c4](https://github.com/home-operations/kritika/commit/069e7c4e97875316b751a252ba5d1ce2287883fa))

## [0.0.51](https://github.com/home-operations/kritika/compare/0.0.50...0.0.51) (2026-10-09)


### Bug Fixes

* **model:** wait out a provider's 402 that carries a Retry-After ([#788](https://github.com/home-operations/kritika/issues/788)) ([ec91eaa](https://github.com/home-operations/kritika/commit/ec91eaaffaf895ce7aa49365cd0f8826ed0cd8b8))
* **npm:** update dependency vite (8.3.2 → 8.3.3) ([#778](https://github.com/home-operations/kritika/issues/778)) ([710a294](https://github.com/home-operations/kritika/commit/710a294b40546b439f86dacf6e35a6178f9c01b6))
* **poller:** take a listing's time from the database clock, and skip the run tool's check off Linux ([#801](https://github.com/home-operations/kritika/issues/801)) ([bab8edf](https://github.com/home-operations/kritika/commit/bab8edfdde19eace06730c03bc02a4ef6f1afe13))
* **review:** keep a finding reported again outside the diff from being listed and resolved as gone ([#796](https://github.com/home-operations/kritika/issues/796)) ([6db569d](https://github.com/home-operations/kritika/commit/6db569dab2ca370572e52cddbe271843bd5a0bc7))
* **review:** leave what the base gained in the change's own files out of an incremental delta ([#793](https://github.com/home-operations/kritika/issues/793)) ([8945f29](https://github.com/home-operations/kritika/commit/8945f299420ad722daad0b787ff2504c0a21c0ba))
* **review:** let a finding dropped as malformed claim no prior finding's thread ([#806](https://github.com/home-operations/kritika/issues/806)) ([4126d19](https://github.com/home-operations/kritika/commit/4126d19ec0477329d26b620eaa3b7aa8b907fe34))
* **review:** let a finding reported again in other words keep its thread ([#792](https://github.com/home-operations/kritika/issues/792)) ([7c49892](https://github.com/home-operations/kritika/commit/7c49892c82f37456bf2600802ffad20dda66f33c))
* **review:** link a finding off the diff to the thread it carries ([#798](https://github.com/home-operations/kritika/issues/798)) ([8770d4e](https://github.com/home-operations/kritika/commit/8770d4e9d09de5fc5ceb7e9f2078a2982fe5384c))
* **review:** record a review's findings off the diff, with the thread each carries ([#797](https://github.com/home-operations/kritika/issues/797)) ([06e0ad6](https://github.com/home-operations/kritika/commit/06e0ad63a409a225fdbd8bb82fdbcb363e98771c))
* **review:** refuse unknown keys in a submission, posting it without them if never corrected ([#790](https://github.com/home-operations/kritika/issues/790)) ([74f4e3d](https://github.com/home-operations/kritika/commit/74f4e3dbee50db0bfab770d77f124800edb7e69a))
* **review:** stop a carried fingerprint from merging findings or reviving a dismissal ([#794](https://github.com/home-operations/kritika/issues/794)) ([e51c0a2](https://github.com/home-operations/kritika/commit/e51c0a2b83017bb1de390527febf6f14650e3613))
* **runner:** log each command as it starts, so a killed run names it ([#789](https://github.com/home-operations/kritika/issues/789)) ([f430832](https://github.com/home-operations/kritika/commit/f4308323d00a7561c81804fdc6dac4cf1e0dab75))
* **runner:** write a .curlrc that drops curl's progress meter and follows redirects ([#787](https://github.com/home-operations/kritika/issues/787)) ([62df97e](https://github.com/home-operations/kritika/commit/62df97ea98ad0b2eea48137451771ea8a3d9e25c))
* **worker:** treat resolving an outdated finding thread as fixed, and give a returning finding a fresh thread ([#791](https://github.com/home-operations/kritika/issues/791)) ([c0b1f44](https://github.com/home-operations/kritika/commit/c0b1f44c155f7214f4856502499f131dfcaf1e19))


### Performance Improvements

* **review:** look a prior finding up by its id or title fingerprint ([#805](https://github.com/home-operations/kritika/issues/805)) ([e627cbf](https://github.com/home-operations/kritika/commit/e627cbf5a32a662194f0b15da3ef046378329504))


### Code Refactoring

* **agent:** say what the deferred fallback rewrites ([#808](https://github.com/home-operations/kritika/issues/808)) ([24b1ac2](https://github.com/home-operations/kritika/commit/24b1ac2a454d3778810e6357b0228fa6a772f3d5))
* **gitfetch:** read a file diff's header behind a flag ([#807](https://github.com/home-operations/kritika/issues/807)) ([5df2a75](https://github.com/home-operations/kritika/commit/5df2a7571e7b91bb674842e0341ad503f60ab31b))
* **gitfetch:** render the delta and the whole diff through one function ([#802](https://github.com/home-operations/kritika/issues/802)) ([02a6122](https://github.com/home-operations/kritika/commit/02a612242ad0652772833c22572909a2157537a3))
* read unified diffs through one package ([#812](https://github.com/home-operations/kritika/issues/812)) ([29f14cf](https://github.com/home-operations/kritika/commit/29f14cf1baf12ae470b63d9ac4c14f0722962786))
* **review:** walk a submission's objects once for its unknown and known keys ([#803](https://github.com/home-operations/kritika/issues/803)) ([21245c7](https://github.com/home-operations/kritika/commit/21245c70f8ec47882e987af58ee3db0ae078df7f))
* **store:** keep Now beside the other Store methods ([#809](https://github.com/home-operations/kritika/issues/809)) ([de7de0f](https://github.com/home-operations/kritika/commit/de7de0f94fc6d6cc73e8453aa8f2c12a810629b9))
* **webhook:** name the outdated-lines rule once ([#804](https://github.com/home-operations/kritika/issues/804)) ([15fd0ef](https://github.com/home-operations/kritika/commit/15fd0efd24726d874e1e8b0d2bb92060b7ee16b2))


### Documentation

* **reviews:** say that findings off the diff are recorded and linked to their thread ([#799](https://github.com/home-operations/kritika/issues/799)) ([3cad5b9](https://github.com/home-operations/kritika/commit/3cad5b91a0ad9bc0429b6c86f5e7e33c31d05391))

## [0.0.50](https://github.com/home-operations/kritika/compare/0.0.49...0.0.50) (2026-10-09)


### Features

* **agent:** keep a cut run output whole in a file ([#770](https://github.com/home-operations/kritika/issues/770)) ([2af07c1](https://github.com/home-operations/kritika/commit/2af07c19a413a92a736e5dffa55def8fd4771726))
* **go:** update anthropic-sdk-go, openai-go and client_golang ([#777](https://github.com/home-operations/kritika/issues/777)) ([a490b36](https://github.com/home-operations/kritika/commit/a490b361cca83ef80a08c346e86fd3a8fbddcacf))
* **npm:** update dependency simple-icons (16.33.0 → 16.34.0) ([#767](https://github.com/home-operations/kritika/issues/767)) ([0b90e3e](https://github.com/home-operations/kritika/commit/0b90e3e34f6c08257253cdcd0b99cfab03ac2bb2))
* **review:** tighten the prompt with rules from published review rubrics ([#776](https://github.com/home-operations/kritika/issues/776)) ([224a5fb](https://github.com/home-operations/kritika/commit/224a5fb8038de0a3486af525fe8595f4d37e8a6b))
* **runner:** keep cut run outputs beside the checkout ([#771](https://github.com/home-operations/kritika/issues/771)) ([78f2337](https://github.com/home-operations/kritika/commit/78f233715f06e2202a11adbd2461dc49de9a1911))


### Documentation

* describe the run tool's kept outputs ([#773](https://github.com/home-operations/kritika/issues/773)) ([6e7ffa9](https://github.com/home-operations/kritika/commit/6e7ffa9d64578e9f33a41589e0f5cbf7a567b7a6))

## [0.0.49](https://github.com/home-operations/kritika/compare/0.0.48...0.0.49) (2026-10-08)


### Code Refactoring

* **bench:** sort the mode names with slices.Sorted ([#762](https://github.com/home-operations/kritika/issues/762)) ([e1080b1](https://github.com/home-operations/kritika/commit/e1080b10250abfe011fe5e581c909585d0442fec))


### Tests

* sleep inside synctest bubbles with synctest.Sleep ([#761](https://github.com/home-operations/kritika/issues/761)) ([8b26cc8](https://github.com/home-operations/kritika/commit/8b26cc8335d01c527c1c010ec3bb9b2c056277ca))
* take the context from the test instead of context.Background ([#764](https://github.com/home-operations/kritika/issues/764)) ([9ad12f3](https://github.com/home-operations/kritika/commit/9ad12f3ac0453ef79b49b59f3b6a8f85dbd0a646))


### Miscellaneous Chores

* bump Go to 1.27.2 and x/net to 0.60.0 for the October advisories ([#766](https://github.com/home-operations/kritika/issues/766)) ([51af759](https://github.com/home-operations/kritika/commit/51af759d90a3c197766c90f36ea257f46915411a))

## [0.0.48](https://github.com/home-operations/kritika/compare/0.0.47...0.0.48) (2026-10-08)


### Features

* **go:** update river monorepo (v0.48.0 → v0.49.0) ([#719](https://github.com/home-operations/kritika/issues/719)) ([9c56856](https://github.com/home-operations/kritika/commit/9c56856d506633ce0ff2a6c48c51ce615c4d401e))
* **main:** log which signal stopped serve ([#757](https://github.com/home-operations/kritika/issues/757)) ([2b0887d](https://github.com/home-operations/kritika/commit/2b0887d4a37b0ea9ef48a81d4d61b733e6aacc94))
* **npm:** update dependency marked (18.0.14 → 18.1.0) ([#759](https://github.com/home-operations/kritika/issues/759)) ([0a5a363](https://github.com/home-operations/kritika/commit/0a5a363c93e3da76d68e8a1400c1f11b0e684ea8))


### Bug Fixes

* **agent:** read a value flag before gh's command as cobra does ([#720](https://github.com/home-operations/kritika/issues/720)) ([ce937e8](https://github.com/home-operations/kritika/commit/ce937e891e50bb93d69dffb7e33cff90ae31da27))
* **config:** refuse bad runner resources at startup ([#731](https://github.com/home-operations/kritika/issues/731)) ([7377e8c](https://github.com/home-operations/kritika/commit/7377e8ccc6ed1b071e68df5b7552da216abb5f84))
* **container:** update image docker.io/jdxcode/mise (2026.10.4 → 2026.10.5) ([#748](https://github.com/home-operations/kritika/issues/748)) ([c637e9e](https://github.com/home-operations/kritika/commit/c637e9e31d73c114b4680e2ef0bdba51f97a2523))
* **contextpack:** read a renamed file's base side under its old name ([#741](https://github.com/home-operations/kritika/issues/741)) ([2808e24](https://github.com/home-operations/kritika/commit/2808e245c68479b3e01a99368e0a01c736faf715))
* **forge:** keep a kilobyte of a refused GraphQL response in the error ([#737](https://github.com/home-operations/kritika/issues/737)) ([12833e9](https://github.com/home-operations/kritika/commit/12833e9f0c77f145b3aac44ed6d3550a6f603c8b))
* **ingest:** settle a head for the poll after two failed reviews ([#726](https://github.com/home-operations/kritika/issues/726)) ([ac66da8](https://github.com/home-operations/kritika/commit/ac66da84d0b02a233cd91a9c710e0a7d35d18430))
* **main:** end the group's goroutines before the store closes ([#729](https://github.com/home-operations/kritika/issues/729)) ([30b28a9](https://github.com/home-operations/kritika/commit/30b28a901bc4562fb9ddf354c6bb1d835da07321))
* **review:** keep the diff's omission note within its reserved room ([#723](https://github.com/home-operations/kritika/issues/723)) ([2e648cc](https://github.com/home-operations/kritika/commit/2e648ccf79ea7b2eb759f32b14e4102bf4818262))
* **review:** keep the pull request's title on its header line ([#739](https://github.com/home-operations/kritika/issues/739)) ([375a7ef](https://github.com/home-operations/kritika/commit/375a7efd0710bb5df05bffda72d5d38c9cf61751))
* **review:** name plaintext secrets in artifacts as a finding access control cannot close ([#760](https://github.com/home-operations/kritika/issues/760)) ([5337fa8](https://github.com/home-operations/kritika/commit/5337fa860de8e5f9bdca3259dd7369a13af9f90b))
* **review:** refuse a Mermaid directive anywhere in a diagram ([#721](https://github.com/home-operations/kritika/issues/721)) ([4aa8329](https://github.com/home-operations/kritika/commit/4aa83296d3d4963dee0d7449ef934f39a95b413c))
* **store:** list the run an earlier attempt left as orphaned at once ([#732](https://github.com/home-operations/kritika/issues/732)) ([699812d](https://github.com/home-operations/kritika/commit/699812df5043c4aeb4ecdacd26edb5ea7ae2266f))
* **store:** record a runner's unknown times as NULL in any time zone ([#728](https://github.com/home-operations/kritika/issues/728)) ([9fc3147](https://github.com/home-operations/kritika/commit/9fc314746ac6d7228421a93f3f60a69f3cc67569))
* **webapi:** bound the span the usage and analytics windows may cover ([#738](https://github.com/home-operations/kritika/issues/738)) ([304db54](https://github.com/home-operations/kritika/commit/304db545a0e5958ef0a5f3609e26e19312c15b63))
* **webapi:** report a cursor whose id is no row id as an invalid cursor ([#753](https://github.com/home-operations/kritika/issues/753)) ([dee6d30](https://github.com/home-operations/kritika/commit/dee6d30a7aa23453de2ded22a6b7aa967d374f9a))
* **worker:** count an automatic review once its summary is posted ([#733](https://github.com/home-operations/kritika/issues/733)) ([68489b8](https://github.com/home-operations/kritika/commit/68489b8eab3ee448edcc44bd82fc00127e69b3d3))
* **worker:** fence a job whose heartbeat stops landing ([#725](https://github.com/home-operations/kritika/issues/725)) ([c3cf809](https://github.com/home-operations/kritika/commit/c3cf80948da6d949d9eda043cf5dd8a97be6968c))
* **worker:** judge a follow-up's unstarted pod by the executor's word ([#749](https://github.com/home-operations/kritika/issues/749)) ([58acbc8](https://github.com/home-operations/kritika/commit/58acbc85fb7880f7d4ba771e26f19f4b16684c0e))
* **worker:** retry a review whose runner pod never started ([#724](https://github.com/home-operations/kritika/issues/724)) ([dfb11da](https://github.com/home-operations/kritika/commit/dfb11da311f39bc8e86f9c2d73a5a242aa4b35db))


### Performance Improvements

* **chunk:** release tree-sitter trees once their declarations are cut ([#727](https://github.com/home-operations/kritika/issues/727)) ([722f257](https://github.com/home-operations/kritika/commit/722f2570fb8de9ac688fe2f4e13401f4e9b94339))
* **indexer:** read each blob once ([#740](https://github.com/home-operations/kritika/issues/740)) ([933716d](https://github.com/home-operations/kritika/commit/933716d320b881309624bd7e95da37416df4dd79))
* **webapi:** keep one GitHub App per connection for the admin's requests ([#742](https://github.com/home-operations/kritika/issues/742)) ([52ae6c0](https://github.com/home-operations/kritika/commit/52ae6c0cd2099ca180e147a031afd2727542e342))


### Code Refactoring

* **chunk:** name the glob matcher for what it says ([#744](https://github.com/home-operations/kritika/issues/744)) ([73dad73](https://github.com/home-operations/kritika/commit/73dad736720694bec216c5d2faf67e12cb686b4b))
* **configfile:** hash the names of the secrets, not their values ([#734](https://github.com/home-operations/kritika/issues/734)) ([33dcca3](https://github.com/home-operations/kritika/commit/33dcca31a002341e47f5aeeba5433e3212bfba13))
* give the mention commands and the system prompts files of their own ([#745](https://github.com/home-operations/kritika/issues/745)) ([025a2c8](https://github.com/home-operations/kritika/commit/025a2c83b142dbf3158530724ef828a1b2cf578b))
* **main:** move retention and the lingering context out of main ([#743](https://github.com/home-operations/kritika/issues/743)) ([0483b52](https://github.com/home-operations/kritika/commit/0483b529b25f9db4c200b714e2e2f55788d4ba34))
* **webapi:** encode and decode the API with encoding/json/v2 ([#751](https://github.com/home-operations/kritika/issues/751)) ([8385894](https://github.com/home-operations/kritika/commit/8385894cf3be0d504cc6c7ffd1eb3bbaaadba751))
* **webapi:** keep each route's response shapes beside its routes ([#752](https://github.com/home-operations/kritika/issues/752)) ([6c93a5b](https://github.com/home-operations/kritika/commit/6c93a5b11a4f1f116c6734e7eaa5c0ac67a667fd))
* **worker:** share the run step between the review and the follow-up ([#750](https://github.com/home-operations/kritika/issues/750)) ([3f809c4](https://github.com/home-operations/kritika/commit/3f809c4d78f36958e34cacb6be3a81a35d5151f2))
* **worker:** share the structured model call between the scorer and the merge ([#746](https://github.com/home-operations/kritika/issues/746)) ([57989d3](https://github.com/home-operations/kritika/commit/57989d3ae1ea7ecfc51971c015fd257333afc07e))


### Miscellaneous Chores

* drop comments that describe code no longer there ([#735](https://github.com/home-operations/kritika/issues/735)) ([a4cfab0](https://github.com/home-operations/kritika/commit/a4cfab0d72391842d5758930f35964823360b191))
* leave draining a response body to Close ([#756](https://github.com/home-operations/kritika/issues/756)) ([ff89998](https://github.com/home-operations/kritika/commit/ff89998f4b3e50125a7460d6314cf81c8050e5b2))
* **runner:** spell the staging columns' slices out ([#755](https://github.com/home-operations/kritika/issues/755)) ([3d4ed28](https://github.com/home-operations/kritika/commit/3d4ed28d8a0e223a77d32c3f351736f1052f5ea9))
* small cleanups the review turned up ([#736](https://github.com/home-operations/kritika/issues/736)) ([742c6b0](https://github.com/home-operations/kritika/commit/742c6b09693608361df3aa03f00651a4c7b21858))
* **webapi:** say Auth is required rather than guard it once ([#754](https://github.com/home-operations/kritika/issues/754)) ([affc6af](https://github.com/home-operations/kritika/commit/affc6af8c3d8675d3a7824cec8874926f307f8a7))

## [0.0.47](https://github.com/home-operations/kritika/compare/0.0.46...0.0.47) (2026-10-08)


### Bug Fixes

* **runner:** write an empty rule list for a split review with no rules ([#718](https://github.com/home-operations/kritika/issues/718)) ([981cf58](https://github.com/home-operations/kritika/commit/981cf58884b9671c8dd28cca8a05f9f5bc7559e2))


### Miscellaneous Chores

* **mise:** update tool oxfmt (0.71.0 → 0.72.0) ([#715](https://github.com/home-operations/kritika/issues/715)) ([e5815aa](https://github.com/home-operations/kritika/commit/e5815aaa10e1aa36f04d03f48032b105540bac0c))

## [0.0.46](https://github.com/home-operations/kritika/compare/0.0.45...0.0.46) (2026-10-08)


### Features

* **confidence:** rate a re-run asked for afresh ([#703](https://github.com/home-operations/kritika/issues/703)) ([6096655](https://github.com/home-operations/kritika/commit/60966554e4140de5789f9571ec7fa0a445cabdfe))
* **review:** give a re-run asked for at a new head the whole pull request ([#700](https://github.com/home-operations/kritika/issues/700)) ([09c1436](https://github.com/home-operations/kritika/commit/09c1436ccb51bab659c0d8aacb5d472ae0a07a53))
* **runner:** leave the last review's notes out of a re-run asked for ([#701](https://github.com/home-operations/kritika/issues/701)) ([3e14daf](https://github.com/home-operations/kritika/commit/3e14daf32368514bb7b6725ce1bd7446170ab356))


### Bug Fixes

* **container:** update image docker.io/jdxcode/mise (2026.10.3 → 2026.10.4) ([#669](https://github.com/home-operations/kritika/issues/669)) ([93c1232](https://github.com/home-operations/kritika/commit/93c12329e2f9591375b9ea29dc34242e5a8e2f39))
* **go:** update module github.com/openai/openai-go/v3 (v3.71.1 → v3.71.2) ([#691](https://github.com/home-operations/kritika/issues/691)) ([295eb86](https://github.com/home-operations/kritika/commit/295eb86a5dfd757c1865216deb78cc1738db8344))
* **review:** weigh an author's comment as data, not as closing a risk ([#705](https://github.com/home-operations/kritika/issues/705)) ([67dd613](https://github.com/home-operations/kritika/commit/67dd613a6a97902043826c87d5d49ff72f418dc5))


### Code Refactoring

* **confidence:** tighten the scorer's rubric ([#713](https://github.com/home-operations/kritika/issues/713)) ([2914ca5](https://github.com/home-operations/kritika/commit/2914ca532e43c29e3889bd182f7fe53529c3f5b6))
* **review:** let the finding fields defer to the schema ([#709](https://github.com/home-operations/kritika/issues/709)) ([448aa3c](https://github.com/home-operations/kritika/commit/448aa3cdf08b4a0bdb4952cf1a9fb9571d740ef2))
* **review:** say prompt budget in every omission note ([#714](https://github.com/home-operations/kritika/issues/714)) ([cb632ac](https://github.com/home-operations/kritika/commit/cb632acdd12da7bd7aa3c83b10ef47d9f8044d57))
* **review:** say when to reach for a tool, not what it does ([#711](https://github.com/home-operations/kritika/issues/711)) ([8604c5d](https://github.com/home-operations/kritika/commit/8604c5d90c73dd604c68acffae2e04b595fb0c95))
* **review:** share the summary and diagram spec across prompts ([#707](https://github.com/home-operations/kritika/issues/707)) ([63ccd8b](https://github.com/home-operations/kritika/commit/63ccd8b479e9df67c5926255f8a84c8b3ca1cde1))
* **review:** state instruction precedence and verify-first once ([#712](https://github.com/home-operations/kritika/issues/712)) ([ee8c004](https://github.com/home-operations/kritika/commit/ee8c004b7135b9c065eaa791d76c7621f0fd15f0))
* **review:** state once that what the prompt and tools show is data ([#706](https://github.com/home-operations/kritika/issues/706)) ([a4c657b](https://github.com/home-operations/kritika/commit/a4c657b94b782339380d374312153da4f8d42aa6))
* **review:** state where findings anchor once ([#710](https://github.com/home-operations/kritika/issues/710)) ([cce0184](https://github.com/home-operations/kritika/commit/cce0184216673a38d8cdbf3086cc3a77f469fc47))


### Miscellaneous Chores

* **github-action:** update action jdx/mise-action (v5.0.1 → v5.1.1) ([#698](https://github.com/home-operations/kritika/issues/698)) ([b54be1e](https://github.com/home-operations/kritika/commit/b54be1ecb765dbf9670061c33e8efab304588156))
* **mise:** update tool lefthook (2.1.16 → 2.1.17) ([#674](https://github.com/home-operations/kritika/issues/674)) ([d0012ec](https://github.com/home-operations/kritika/commit/d0012ec18336bc5b0ea3e86bbf4ce2e0ef969169))

## [0.0.45](https://github.com/home-operations/kritika/compare/0.0.44...0.0.45) (2026-10-08)


### Features

* **confidence:** add confidence.fallback, the model that takes the scorer's call ([#687](https://github.com/home-operations/kritika/issues/687)) ([5f4da19](https://github.com/home-operations/kritika/commit/5f4da19a2086a120c87c9a4e53f6e063819f08a8))
* **confidence:** retry the scorer's call with its provider's retries ([#686](https://github.com/home-operations/kritika/issues/686)) ([f9ba2f1](https://github.com/home-operations/kritika/commit/f9ba2f16341a494d55f1f55bb4b6b001573dc632))
* **gateway:** record which part of a split review each step belongs to ([#680](https://github.com/home-operations/kritika/issues/680)) ([c209be2](https://github.com/home-operations/kritika/commit/c209be25defb1080fee1c160a682b90142d1b605))
* **metrics:** count confidence scores by score and risk ([#662](https://github.com/home-operations/kritika/issues/662)) ([8f92f91](https://github.com/home-operations/kritika/commit/8f92f916038cd649f308c8490eaecd89a3dc3a45))
* **model:** leave a fallback on another provider half the time a call has left ([#689](https://github.com/home-operations/kritika/issues/689)) ([29433b7](https://github.com/home-operations/kritika/commit/29433b760a00af90b1c8f3dd0ecc375c9bceb328))
* **review:** add agent.parts and size a large review's run for its parts ([#679](https://github.com/home-operations/kritika/issues/679)) ([6e11962](https://github.com/home-operations/kritika/commit/6e1196275370d45c3ec121e6c15940b6a0a79724))
* **review:** carry a pull request's risk across re-reviews ([#666](https://github.com/home-operations/kritika/issues/666)) ([ccbb15f](https://github.com/home-operations/kritika/commit/ccbb15fe7bcf03d42cde060d65a826909bb65d11))
* **review:** cut a large diff into the parts of a split review ([#676](https://github.com/home-operations/kritika/issues/676)) ([e89b7e3](https://github.com/home-operations/kritika/commit/e89b7e34ecd99160e3bdc9be5e6e15e71cfe085f))
* **review:** mark new, deleted and renamed files, and review left-out ones ([#671](https://github.com/home-operations/kritika/issues/671)) ([6410ad8](https://github.com/home-operations/kritika/commit/6410ad847c0f2d78fc2a72fa6df90a8e7f2f4a60))
* **review:** read a changed file's part of the diff with read_diff ([#673](https://github.com/home-operations/kritika/issues/673)) ([789759d](https://github.com/home-operations/kritika/commit/789759d3678f40ca10aba504ca481d02179e9be4))
* **review:** retry a split review's summary call and let review.fallback take it ([#690](https://github.com/home-operations/kritika/issues/690)) ([e4aa3fd](https://github.com/home-operations/kritika/commit/e4aa3fd067337706de158b2fd7426a4931ec0701))
* **review:** review a large diff in parts, one agent after another ([#681](https://github.com/home-operations/kritika/issues/681)) ([6a74fa5](https://github.com/home-operations/kritika/commit/6a74fa594b97597a3ea96940a2953fc58a765ca7))
* **review:** run a split review's parts at once in the model slots free ([#684](https://github.com/home-operations/kritika/issues/684)) ([e1979fd](https://github.com/home-operations/kritika/commit/e1979fd0b901678f8d5a3a71dff7cedbcb0059af))
* **review:** write a split review's summary from its parts' in one call ([#682](https://github.com/home-operations/kritika/issues/682)) ([c370626](https://github.com/home-operations/kritika/commit/c37062690a4b6a7151df6499c81bc365dc70687b))
* **web:** show a split review's parts on its timeline and conversation ([#685](https://github.com/home-operations/kritika/issues/685)) ([2d1fbc2](https://github.com/home-operations/kritika/commit/2d1fbc213cbce45c68d5c14526d8abf9ef12b8d6))


### Bug Fixes

* **agent:** go on after a step cut off before any tool call ([#670](https://github.com/home-operations/kritika/issues/670)) ([984ea5e](https://github.com/home-operations/kritika/commit/984ea5ed86eea7f7e8fc52467f4aba7e7cd6133f))
* **review:** keep what the scorer cannot verify out of risk ([#665](https://github.com/home-operations/kritika/issues/665)) ([d2d1f4b](https://github.com/home-operations/kritika/commit/d2d1f4b7156ee077cc31a11690d8ea986f39896d))
* **review:** rate risk by what a change can do, and an update by its step ([#663](https://github.com/home-operations/kritika/issues/663)) ([4c74bcb](https://github.com/home-operations/kritika/commit/4c74bcb33b9d4223bac1ac48b65ef6d47c4e8255))

## [0.0.44](https://github.com/home-operations/kritika/compare/0.0.43...0.0.44) (2026-10-08)


### Features

* **review:** review a merged or closed pull request someone asks for ([#660](https://github.com/home-operations/kritika/issues/660)) ([c0f5722](https://github.com/home-operations/kritika/commit/c0f57223cd32cf1b82666da87b3cd5fe9501f0fa))


### Bug Fixes

* **review:** drop the empty paths a fetch_repo call gives ([#658](https://github.com/home-operations/kritika/issues/658)) ([83e17fd](https://github.com/home-operations/kritika/commit/83e17fd7614319d192decae1a40b4d98f535aa14))

## [0.0.43](https://github.com/home-operations/kritika/compare/0.0.42...0.0.43) (2026-10-07)


### Features

* **agent:** answer a gh call without its command with the likely one ([#652](https://github.com/home-operations/kritika/issues/652)) ([436fef9](https://github.com/home-operations/kritika/commit/436fef9305d2b27e2757227fba5ea4904dd6157b))
* **review:** let read_file read the files fetch_repo wrote ([#653](https://github.com/home-operations/kritika/issues/653)) ([5617d3a](https://github.com/home-operations/kritika/commit/5617d3a06cbbbb0bcef4e5f5f512a88be7584891))
* **review:** name where a path fetch_repo found nothing under is ([#654](https://github.com/home-operations/kritika/issues/654)) ([f82a889](https://github.com/home-operations/kritika/commit/f82a88953883b56bea4ab412d757213eda3e9f34))
* **review:** show the confidence scorer what the review read ([#651](https://github.com/home-operations/kritika/issues/651)) ([6675909](https://github.com/home-operations/kritika/commit/66759090324e101fa72b72651d7533a283232265))


### Bug Fixes

* **agent:** keep the end of a command's stderr when its output is cut ([#649](https://github.com/home-operations/kritika/issues/649)) ([c55c954](https://github.com/home-operations/kritika/commit/c55c9546435e0f036c40bb71d05794a3c6f9711c))
* **review:** list only the sources a review read something from ([#655](https://github.com/home-operations/kritika/issues/655)) ([2074d90](https://github.com/home-operations/kritika/commit/2074d900d7cff8e5c66ab5bd01c5bab5b21bc7c4))
* **review:** read an empty fetch_repo tags beside a ref as no listing ([#648](https://github.com/home-operations/kritika/issues/648)) ([5e519f8](https://github.com/home-operations/kritika/commit/5e519f84a5450d418cc97e0188fe685059e22473))


### Performance Improvements

* **model:** leave the confidence call out of the prompt cache ([#656](https://github.com/home-operations/kritika/issues/656)) ([dfe7ec2](https://github.com/home-operations/kritika/commit/dfe7ec2307f99fb75066f685dd25dc4bfaefd71a))

## [0.0.42](https://github.com/home-operations/kritika/compare/0.0.41...0.0.42) (2026-10-07)


### Features

* **review:** let fetch_repo list a repository's tags ([#645](https://github.com/home-operations/kritika/issues/645)) ([34abc70](https://github.com/home-operations/kritika/commit/34abc70f629002fcbcd27c81f10b209ac931f75b))
* **review:** send a version bump's diff to fetch_repo, not the compare view ([#642](https://github.com/home-operations/kritika/issues/642)) ([f28f0de](https://github.com/home-operations/kritika/commit/f28f0de502dfc344510cadc0dd72bbd57381eb00))
* **upstream:** list a repository's tags ([#644](https://github.com/home-operations/kritika/issues/644)) ([ad88579](https://github.com/home-operations/kritika/commit/ad88579a525844ca179020a25dcec47c5172ab57))


### Bug Fixes

* **agent:** keep a cut tool output's note within the output limit ([#641](https://github.com/home-operations/kritika/issues/641)) ([a794a80](https://github.com/home-operations/kritika/commit/a794a80cb5783361e6dfddd06941a575048c3b64))

## [0.0.41](https://github.com/home-operations/kritika/compare/0.0.40...0.0.41) (2026-10-07)


### Features

* **agent:** send a version bump to gh's compare view, narrowed with --jq ([#635](https://github.com/home-operations/kritika/issues/635)) ([593b7af](https://github.com/home-operations/kritika/commit/593b7af9c25f4711333fc527a3d00335cb054c64))
* **index:** drop the index of any repository that stops running ([#628](https://github.com/home-operations/kritika/issues/628)) ([5b48d40](https://github.com/home-operations/kritika/commit/5b48d405e7ece3f36ca65539642d353f736d3c9d))
* **ingest:** disable a repository GitHub deleted, renamed or transferred ([#629](https://github.com/home-operations/kritika/issues/629)) ([d62a7fa](https://github.com/home-operations/kritika/commit/d62a7fa2fe6bae62ac2f107de7bcd108e8c6e7ff))
* **poller:** disable the repositories an App no longer lists ([#631](https://github.com/home-operations/kritika/issues/631)) ([2520aef](https://github.com/home-operations/kritika/commit/2520aef2690055570c51ebf78b24f1a4cabfa6b4))
* **review:** a review leaves notes on what it checked for the next one ([#620](https://github.com/home-operations/kritika/issues/620)) ([f27c75d](https://github.com/home-operations/kritika/commit/f27c75deb9cf02a2b95b43f9a2b90b6d4f6b9741))
* **review:** carry on the last review's conversation on a push ([#624](https://github.com/home-operations/kritika/issues/624)) ([c23957e](https://github.com/home-operations/kritika/commit/c23957e17dd3a5c8dc5daf73935ae4bdf99cf71e))
* **review:** give the agent fetch_repo to search another repository's files ([#638](https://github.com/home-operations/kritika/issues/638)) ([f02807c](https://github.com/home-operations/kritika/commit/f02807cafbb0fed187bb5bd081f083c2b55c17b2))
* **review:** hold back a re-review's new findings off the new commits ([#617](https://github.com/home-operations/kritika/issues/617)) ([1bc9e0b](https://github.com/home-operations/kritika/commit/1bc9e0b6b5e1d4187d6302ecee04f2b37f8f53ef))
* **review:** show a full re-review the last review's findings ([#618](https://github.com/home-operations/kritika/issues/618)) ([a0e17c0](https://github.com/home-operations/kritika/commit/a0e17c0a79038b616b4cf6ca85b7c224767fa5c8))
* **runner:** keep a review agent's conversation for the next re-review ([#621](https://github.com/home-operations/kritika/issues/621)) ([252dcf0](https://github.com/home-operations/kritika/commit/252dcf049ae2dad0104a804461445508de614599))
* **upstream:** fetch another repository's files at a ref, filtered to paths ([#636](https://github.com/home-operations/kritika/issues/636)) ([254feed](https://github.com/home-operations/kritika/commit/254feedb785943aab02295a3680c807ef89ad589))
* **web:** show a review that carried on the one before ([#626](https://github.com/home-operations/kritika/issues/626)) ([0bcc790](https://github.com/home-operations/kritika/commit/0bcc790d37cab86e82824eaede687860600ab31b))


### Build System

* **deps:** move to go.yaml.in/yaml/v4 v4.0.0-rc.6 ([#633](https://github.com/home-operations/kritika/issues/633)) ([2378e95](https://github.com/home-operations/kritika/commit/2378e95796d49112c41b527c551f58d669942512))
* **deps:** upgrade go-git to v6.0.0-beta.1 ([#632](https://github.com/home-operations/kritika/issues/632)) ([b0ee777](https://github.com/home-operations/kritika/commit/b0ee777998e798dc369e5345c5f7b7a74641812e))

## [0.0.40](https://github.com/home-operations/kritika/compare/0.0.39...0.0.40) (2026-10-07)


### ⚠ BREAKING CHANGES

* **config:** drop review.feedback; every review is detailed ([#606](https://github.com/home-operations/kritika/issues/606))

### Features

* **config:** drop review.feedback; every review is detailed ([#606](https://github.com/home-operations/kritika/issues/606)) ([a2476cb](https://github.com/home-operations/kritika/commit/a2476cb912f9fdb986589dfd48a2761d7e42bbc5))
* **config:** review.effort and confidence.effort ([#611](https://github.com/home-operations/kritika/issues/611)) ([e59e129](https://github.com/home-operations/kritika/commit/e59e129575c61ad89dca208961aba2b6588f3056))
* **gateway:** run tokens carry the review's effort ([#613](https://github.com/home-operations/kritika/issues/613)) ([7eb7f30](https://github.com/home-operations/kritika/commit/7eb7f30ebab942d6f4d39692f3c2d06d02ea0e62))
* **model:** carry a reasoning effort to each provider ([#610](https://github.com/home-operations/kritika/issues/610)) ([e6ffe9c](https://github.com/home-operations/kritika/commit/e6ffe9c73481677ecd68c7aeadddb3b61d9b5891))
* **review:** name the effort beside the model in the footers ([#616](https://github.com/home-operations/kritika/issues/616)) ([cc83ed7](https://github.com/home-operations/kritika/commit/cc83ed7763102582881c3ee0159eaee5e5494e3a))
* **web:** show the review and confidence effort on the repository page ([#614](https://github.com/home-operations/kritika/issues/614)) ([59fcb02](https://github.com/home-operations/kritika/commit/59fcb022223856d3005c6b5d748b09fe68aec239))


### Bug Fixes

* **review:** count findings outside the diff ([#607](https://github.com/home-operations/kritika/issues/607)) ([3d08929](https://github.com/home-operations/kritika/commit/3d0892968bad1ea4a432dedd5d966054c1f33aa0))


### Documentation

* break the configuration page into tables and sections ([#599](https://github.com/home-operations/kritika/issues/599)) ([36c26c6](https://github.com/home-operations/kritika/commit/36c26c63c52ab98a4b9b0bc1c36eba3a629dfc92))
* correct claims that no longer match the code ([#595](https://github.com/home-operations/kritika/issues/595)) ([e6455fb](https://github.com/home-operations/kritika/commit/e6455fb88db1abc1ee902ae707c9b17efff75a63))
* describe review.effort and confidence.effort ([#615](https://github.com/home-operations/kritika/issues/615)) ([f849ec9](https://github.com/home-operations/kritika/commit/f849ec94635584465537318f5808e2c0cddaf59d))
* gather the runner's security model on a Security page ([#602](https://github.com/home-operations/kritika/issues/602)) ([efe70b2](https://github.com/home-operations/kritika/commit/efe70b2d86e79c8ec5f9f9dd28e6d90ee3786927))
* gather the score, risk and approvals on one page ([#604](https://github.com/home-operations/kritika/issues/604)) ([f4e0e7d](https://github.com/home-operations/kritika/commit/f4e0e7d7d8958964f90ec08352c16b20d9fed207))
* gather what a review posts and the bot's commands on one page ([#596](https://github.com/home-operations/kritika/issues/596)) ([c99abb2](https://github.com/home-operations/kritika/commit/c99abb243a43e948c3db0f62e180bf73f7c57423))
* give each .kritika.yaml key its own section and tables ([#600](https://github.com/home-operations/kritika/issues/600)) ([efaf954](https://github.com/home-operations/kritika/commit/efaf9543980484d78d4333bfc39bdc906d719765))
* move providers, retries and the embedder to a Models page ([#603](https://github.com/home-operations/kritika/issues/603)) ([d76ac7b](https://github.com/home-operations/kritika/commit/d76ac7bd1a6214d0f6136c5cf6cec2fc3adc3023))
* put setup in the order it is done, with tables ([#598](https://github.com/home-operations/kritika/issues/598)) ([0259c9d](https://github.com/home-operations/kritika/commit/0259c9d865940e89294660fdf0dd5f24a4d03b8a))
* use tables and lists on the dashboard, database and README pages ([#601](https://github.com/home-operations/kritika/issues/601)) ([06f932b](https://github.com/home-operations/kritika/commit/06f932b4a8cd33177fc17eb39ae3549a79bcf6ef))

## [0.0.39](https://github.com/home-operations/kritika/compare/0.0.38...0.0.39) (2026-10-06)


### Features

* **dashboard:** say what a review costs under the month's spend ([#590](https://github.com/home-operations/kritika/issues/590)) ([418667b](https://github.com/home-operations/kritika/commit/418667b9d858f1757df051093427fc608358da2a))
* **review:** end the summary's footer with the pull request's cost ([#593](https://github.com/home-operations/kritika/issues/593)) ([43076f1](https://github.com/home-operations/kritika/commit/43076f1072804823f827e882c72f6452bf2a482d))
* **review:** say in the summary whether the pull request was approved ([#591](https://github.com/home-operations/kritika/issues/591)) ([c6d9e2a](https://github.com/home-operations/kritika/commit/c6d9e2a6d742bac75bb9b9775b8de76d049e97bf))

## [0.0.38](https://github.com/home-operations/kritika/compare/0.0.37...0.0.38) (2026-10-06)


### Features

* **review:** react to the pull request while reviewing and once reviewed ([#588](https://github.com/home-operations/kritika/issues/588)) ([73380b9](https://github.com/home-operations/kritika/commit/73380b9ae7583eda2867e9026df2908863a75ed9))

## [0.0.37](https://github.com/home-operations/kritika/compare/0.0.36...0.0.37) (2026-10-06)


### Features

* **agent:** make the review prompt budget configurable ([#582](https://github.com/home-operations/kritika/issues/582)) ([519d306](https://github.com/home-operations/kritika/commit/519d3069bf47bef55c2add4a4cbdc24216c4ba84))
* **followup:** answer follow-ups with an agent in a runner ([#584](https://github.com/home-operations/kritika/issues/584)) ([df7786f](https://github.com/home-operations/kritika/commit/df7786f67c54ca3d63c195e35bf130bd9fd594b8))
* **followup:** leave eyes on a mention while its agent works ([#586](https://github.com/home-operations/kritika/issues/586)) ([56d9fa5](https://github.com/home-operations/kritika/commit/56d9fa5d1967284df0307b497aa30e04ac2a60b3))


### Code Refactoring

* **agent:** name the submit tool in the loop's own messages ([#583](https://github.com/home-operations/kritika/issues/583)) ([3c1357f](https://github.com/home-operations/kritika/commit/3c1357f7ce3b912ed14f13bbbf2d80bef18b5b30))

## [0.0.36](https://github.com/home-operations/kritika/compare/0.0.35...0.0.36) (2026-10-06)


### Bug Fixes

* **chart:** let the sign-in client ids come from a Secret ([#579](https://github.com/home-operations/kritika/issues/579)) ([cc826bf](https://github.com/home-operations/kritika/commit/cc826bfb97b45f2905a8befe6d65c795e56b8eea))

## [0.0.35](https://github.com/home-operations/kritika/compare/0.0.34...0.0.35) (2026-10-06)


### Bug Fixes

* **review:** keep the summary diagram across incremental reviews ([#576](https://github.com/home-operations/kritika/issues/576)) ([9df9a8e](https://github.com/home-operations/kritika/commit/9df9a8e283c497082616e865cf9be5da3773f3df))

## [0.0.34](https://github.com/home-operations/kritika/compare/0.0.33...0.0.34) (2026-10-06)


### ⚠ BREAKING CHANGES

* **egress:** egress.allow and egress.deny replace egress.allowHosts ([#568](https://github.com/home-operations/kritika/issues/568))

### Features

* **egress:** address entries, and private addresses refused unless allowed ([#570](https://github.com/home-operations/kritika/issues/570)) ([e389923](https://github.com/home-operations/kritika/commit/e3899238faac3685b6e5029a9be2ff832de9b016))
* **egress:** egress.allow and egress.deny replace egress.allowHosts ([#568](https://github.com/home-operations/kritika/issues/568)) ([0de6b66](https://github.com/home-operations/kritika/commit/0de6b66fa12bc4bce1560787552864de5bd342fb))
* **review:** count the reviews and name the head commit in the comment footer ([#574](https://github.com/home-operations/kritika/issues/574)) ([b0a8a8d](https://github.com/home-operations/kritika/commit/b0a8a8d726ead170b47ef765a5ddeec811161883))


### Bug Fixes

* **review:** draw summary diagrams as plain-language flows ([#569](https://github.com/home-operations/kritika/issues/569)) ([8d92c81](https://github.com/home-operations/kritika/commit/8d92c818e623fa2e00d5ff3f53a2428f03eeeb80))


### Documentation

* **chart:** describe the NetworkPolicies as rendered ([#573](https://github.com/home-operations/kritika/issues/573)) ([9d95fa3](https://github.com/home-operations/kritika/commit/9d95fa3345a446ae1e89ae4719f64d0570d3d06d))

## [0.0.33](https://github.com/home-operations/kritika/compare/0.0.32...0.0.33) (2026-10-06)


### Features

* **review:** say which commands the run tool offered and the agent ran ([#565](https://github.com/home-operations/kritika/issues/565)) ([0d1a9c8](https://github.com/home-operations/kritika/commit/0d1a9c8c0852fa41e21fdd585b9344971d8fdce1))


### Bug Fixes

* **review:** state the confidence score without the threshold it fell short of ([#564](https://github.com/home-operations/kritika/issues/564)) ([ff99778](https://github.com/home-operations/kritika/commit/ff99778ac702788341dda4d83dae746f24e91afc))

## [0.0.32](https://github.com/home-operations/kritika/compare/0.0.31...0.0.32) (2026-10-06)


### Features

* **review:** draw the change's flow as a mermaid diagram in the summary ([#562](https://github.com/home-operations/kritika/issues/562)) ([0650544](https://github.com/home-operations/kritika/commit/0650544157f29abf86e4edaf8fb8dd4a92eefb99))

## [0.0.31](https://github.com/home-operations/kritika/compare/0.0.30...0.0.31) (2026-10-06)


### Features

* **egress:** let "*" in egress.allowHosts allow every host ([#558](https://github.com/home-operations/kritika/issues/558)) ([8672009](https://github.com/home-operations/kritika/commit/867200959c1d8a9b796fd6ac9483a8c5d91f2f79))
* **runner:** mark the run tool's checkout as a repository with the clone URL as origin ([#559](https://github.com/home-operations/kritika/issues/559)) ([c8ba64b](https://github.com/home-operations/kritika/commit/c8ba64b5710358e12f2e3487a66f26a3911f5494))

## [0.0.30](https://github.com/home-operations/kritika/compare/0.0.29...0.0.30) (2026-10-06)


### Features

* **agent:** retry a step the model was told to submit on, instead of ending the run at its first slip ([#555](https://github.com/home-operations/kritika/issues/555)) ([fadf200](https://github.com/home-operations/kritika/commit/fadf20005344887f53efa14cdc5c94333f9d37f9))
* **config:** gate the commit status on the confidence score only where asked ([#552](https://github.com/home-operations/kritika/issues/552)) ([9e243a3](https://github.com/home-operations/kritika/commit/9e243a38729e0a8ab83b4f61fe2e38097bb6959a))


### Bug Fixes

* **store:** let an agent run be recorded as truncated ([#556](https://github.com/home-operations/kritika/issues/556)) ([ae6b441](https://github.com/home-operations/kritika/commit/ae6b44176e47163051a2d3cbef1b403d58d1b421))


### Miscellaneous Chores

* **mise:** let the test task take packages, and run the integration task on the integration packages alone ([#553](https://github.com/home-operations/kritika/issues/553)) ([5f11d2c](https://github.com/home-operations/kritika/commit/5f11d2c4eabe5c4ad2b16fbaf8431b9043277ba2))

## [0.0.29](https://github.com/home-operations/kritika/compare/0.0.28...0.0.29) (2026-10-06)


### Bug Fixes

* **review:** keep an incremental review's delta to the change's own paths, and look afresh at a rebase that moved none ([#549](https://github.com/home-operations/kritika/issues/549)) ([b176a48](https://github.com/home-operations/kritika/commit/b176a480c3f8b233eda391dac8380d6c71d28e77))

## [0.0.28](https://github.com/home-operations/kritika/compare/0.0.27...0.0.28) (2026-10-06)


### Features

* **web:** list the skills a review was offered and the ones it read ([#545](https://github.com/home-operations/kritika/issues/545)) ([f96ab3f](https://github.com/home-operations/kritika/commit/f96ab3febc3108f37af8b4aef958f7b0f1fa8740))
* **web:** show a review's confidence, risk and reason on the pull request and its summary ([#544](https://github.com/home-operations/kritika/issues/544)) ([f9ada01](https://github.com/home-operations/kritika/commit/f9ada01e59f56692015a3390310c7dd932f481f8))


### Bug Fixes

* **container:** update image docker.io/jdxcode/mise (2026.10.1 → 2026.10.3) ([#547](https://github.com/home-operations/kritika/issues/547)) ([9936d45](https://github.com/home-operations/kritika/commit/9936d45ec68bc75fcae4b77a91a4ea0216ebaab3))
* **review:** keep the confidence reason off the change itself ([#541](https://github.com/home-operations/kritika/issues/541)) ([3914f49](https://github.com/home-operations/kritika/commit/3914f4934b0a4eb071d528f3276c06e97007c87a))
* **review:** rate a dependency update by its dependency and the size of the move ([#548](https://github.com/home-operations/kritika/issues/548)) ([83c1a92](https://github.com/home-operations/kritika/commit/83c1a920ed368d1ff7fc033905612423141f35c4))
* **web:** name the root settings the instance's, and say a fork is reviewed like any other ([#543](https://github.com/home-operations/kritika/issues/543)) ([a7de93b](https://github.com/home-operations/kritika/commit/a7de93b188d9b747bde5f56e5adea15d4162a489))

## [0.0.27](https://github.com/home-operations/kritika/compare/0.0.26...0.0.27) (2026-10-06)


### ⚠ BREAKING CHANGES

* **config:** give a rule its conditions as a when list ([#530](https://github.com/home-operations/kritika/issues/530))
* **config:** let a trigger condition judge the diff ([#529](https://github.com/home-operations/kritika/issues/529))
* **config:** write the ignore globs at the root ([#528](https://github.com/home-operations/kritika/issues/528))
* **config:** decide which pull requests are reviewed with include and exclude lists ([#526](https://github.com/home-operations/kritika/issues/526))
* **config:** always read a repository's agent files ([#521](https://github.com/home-operations/kritika/issues/521))
* **config:** group the repository settings and drop the defaults wrapper ([#520](https://github.com/home-operations/kritika/issues/520))

### Features

* **config:** always read a repository's agent files ([#521](https://github.com/home-operations/kritika/issues/521)) ([549baf7](https://github.com/home-operations/kritika/commit/549baf75e9837435ad0a6838fede85bf92fc55b1))
* **config:** decide which pull requests are reviewed with include and exclude lists ([#526](https://github.com/home-operations/kritika/issues/526)) ([407509b](https://github.com/home-operations/kritika/commit/407509bd673b052cfaeb04feebf51f70b7f74352))
* **config:** give a rule its conditions as a when list ([#530](https://github.com/home-operations/kritika/issues/530)) ([649d768](https://github.com/home-operations/kritika/commit/649d768c1535341e1ebf7602f33f0be87701fac5))
* **config:** group the repository settings and drop the defaults wrapper ([#520](https://github.com/home-operations/kritika/issues/520)) ([68cb910](https://github.com/home-operations/kritika/commit/68cb9108480068f46d9aed58509954b87ea588c0))
* **config:** let a trigger condition judge the diff ([#529](https://github.com/home-operations/kritika/issues/529)) ([1537e15](https://github.com/home-operations/kritika/commit/1537e151ba750604f464dc726004a506f12b5f82))
* **config:** write the ignore globs at the root ([#528](https://github.com/home-operations/kritika/issues/528)) ([342b568](https://github.com/home-operations/kritika/commit/342b568ac75f8ad2a2814a3f71403229d912cb77))
* **review:** approve on the confidence score and the change's risk ([#525](https://github.com/home-operations/kritika/issues/525)) ([93753b6](https://github.com/home-operations/kritika/commit/93753b65a8ee30f4d1995eee9ded24170c19d360))
* **review:** offer a review the repository's skills ([#531](https://github.com/home-operations/kritika/issues/531)) ([1e8ec5a](https://github.com/home-operations/kritika/commit/1e8ec5a3d59e72eb1f3e2137fb658275180cf331))
* **review:** rate a reviewed change's risk ([#524](https://github.com/home-operations/kritika/issues/524)) ([81385ea](https://github.com/home-operations/kritika/commit/81385eafe3b703dd5c0ef9d9ec5ff2342a712c8f))
* **review:** score a reviewed pull request's confidence ([#523](https://github.com/home-operations/kritika/issues/523)) ([8e70d44](https://github.com/home-operations/kritika/commit/8e70d441514a7232ea6bd8525d28bd62f6301f63))


### Bug Fixes

* **gateway:** let the runner outwait the gateway's step budget ([#537](https://github.com/home-operations/kritika/issues/537)) ([b5acfc5](https://github.com/home-operations/kritika/commit/b5acfc53c986124964d148881cd071aac6c80347))
* **poller:** close the pull requests whose closed event was missed ([#533](https://github.com/home-operations/kritika/issues/533)) ([d90a59a](https://github.com/home-operations/kritika/commit/d90a59a42d4f0eb750e6f9e71e998f1c4e2e21d3))


### Code Refactoring

* match error types with errors.AsType ([#540](https://github.com/home-operations/kritika/issues/540)) ([0499729](https://github.com/home-operations/kritika/commit/04997297bf8ddf3fe9a00aac9fa77f28daa5015b))
* **poller:** look the forge's open pull requests up in a set ([#538](https://github.com/home-operations/kritika/issues/538)) ([0ba310a](https://github.com/home-operations/kritika/commit/0ba310a8e4e32ad0861769d538c62ca42c3d5010))

## [0.0.26](https://github.com/home-operations/kritika/compare/0.0.25...0.0.26) (2026-10-05)


### Features

* **ingest:** start a review when a label change lets one through ([#517](https://github.com/home-operations/kritika/issues/517)) ([b95c996](https://github.com/home-operations/kritika/commit/b95c9965ec7b82f26c09a4672d440cd782628862))
* **worker:** record a label change's repeated skip once ([#516](https://github.com/home-operations/kritika/issues/516)) ([53004e2](https://github.com/home-operations/kritika/commit/53004e2e230ac6548542fe1243291ffd2d26cdbe))

## [0.0.25](https://github.com/home-operations/kritika/compare/0.0.24...0.0.25) (2026-10-05)


### Bug Fixes

* **web:** call the settings' automatic choices Auto and the menu link Settings ([#513](https://github.com/home-operations/kritika/issues/513)) ([a0d1481](https://github.com/home-operations/kritika/commit/a0d1481405b8c51135258f90c13df02dfbb9011d))
* **web:** keep the tab strips from scrolling vertically ([#515](https://github.com/home-operations/kritika/issues/515)) ([cff1249](https://github.com/home-operations/kritika/commit/cff1249d727b75918e1fda6adf37e3d03d9dfed9))

## [0.0.24](https://github.com/home-operations/kritika/compare/0.0.23...0.0.24) (2026-10-05)


### Features

* **web:** say when a pull request last synchronized and keep skips out of its history ([#506](https://github.com/home-operations/kritika/issues/506)) ([40ea7f0](https://github.com/home-operations/kritika/commit/40ea7f0155ad58e7c6e96fb017b712a61f5e81eb))
* **web:** take a pull request's last review from those not skipped ([#508](https://github.com/home-operations/kritika/issues/508)) ([bf7b7a0](https://github.com/home-operations/kritika/commit/bf7b7a025e10041368fa42f774173e3f2355b6cd))
* **web:** take a repository's last review from those not skipped ([#511](https://github.com/home-operations/kritika/issues/511)) ([48f237d](https://github.com/home-operations/kritika/commit/48f237dc03f1653b768354e156bc16d393f315fd))


### Bug Fixes

* **web:** count only completed reviews in the last 7 days ([#505](https://github.com/home-operations/kritika/issues/505)) ([bb599fe](https://github.com/home-operations/kritika/commit/bb599fe0c1568d679ae54953bfe8d07a7c73e010))
* **web:** do not call a skipped review a newer review ([#509](https://github.com/home-operations/kritika/issues/509)) ([74dce02](https://github.com/home-operations/kritika/commit/74dce02fa50e77a00c6f0d3b028ca1228fc5a0ca))
* **web:** say why a pull request was not reviewed when the skip has no reason of its own ([#510](https://github.com/home-operations/kritika/issues/510)) ([117fac9](https://github.com/home-operations/kritika/commit/117fac92c90dcdb01acf64db94c25b64dc9496f1))

## [0.0.23](https://github.com/home-operations/kritika/compare/0.0.22...0.0.23) (2026-10-05)


### Features

* **web:** say what runs now on the overview ([#502](https://github.com/home-operations/kritika/issues/502)) ([06b3d3a](https://github.com/home-operations/kritika/commit/06b3d3ad5148d56072ca16e3ee0bf6c18ffd4877))
* **web:** show each account's reviews today against its daily cap ([#504](https://github.com/home-operations/kritika/issues/504)) ([c8b79cb](https://github.com/home-operations/kritika/commit/c8b79cb61ac3d4353662d3f97856ccb955180017))
* **web:** total what needs attention on the overview ([#501](https://github.com/home-operations/kritika/issues/501)) ([44a6c1d](https://github.com/home-operations/kritika/commit/44a6c1d89fa054d330c66cc0f982c79d6fea0327))


### Bug Fixes

* **web:** break a long path after a slash or a hyphen ([#499](https://github.com/home-operations/kritika/issues/499)) ([1855c69](https://github.com/home-operations/kritika/commit/1855c6990364cde5e2e2dc443e1b9dfb38df4dca))
* **web:** drop the zero minutes from a duration in hours ([#490](https://github.com/home-operations/kritika/issues/490)) ([c783b91](https://github.com/home-operations/kritika/commit/c783b916d3d056633c29acdfd47a0c943a9ade12))
* **web:** fit the tables of accounts in their cards ([#494](https://github.com/home-operations/kritika/issues/494)) ([75921ac](https://github.com/home-operations/kritika/commit/75921acb31d13db0b9123e8cb737264dac190a35))
* **web:** give a chart's axis room for its longest label ([#498](https://github.com/home-operations/kritika/issues/498)) ([0ee69d8](https://github.com/home-operations/kritika/commit/0ee69d820ebd80fed3afe4512797b46846bc4ea5))
* **web:** group the digits of a repository's limits ([#491](https://github.com/home-operations/kritika/issues/491)) ([f857712](https://github.com/home-operations/kritika/commit/f8577126a33d65600cc581c38d253a80ab124bec))
* **web:** group the digits of counts and of money in tables ([#497](https://github.com/home-operations/kritika/issues/497)) ([23a5c72](https://github.com/home-operations/kritika/commit/23a5c72181e23d365ed5df4b222bda8117b19c58))
* **web:** keep a job's error readable beside wide columns ([#493](https://github.com/home-operations/kritika/issues/493)) ([99609ba](https://github.com/home-operations/kritika/commit/99609baa1d3d5cce7d5ab8acc7d35b647d16d1ee))
* **web:** keep a rule readable in the rules table on a phone ([#489](https://github.com/home-operations/kritika/issues/489)) ([b9a44de](https://github.com/home-operations/kritika/commit/b9a44de31081d1fd2a076ea3fe76bdd7eb9e4542))
* **web:** keep the audit log's rows one height ([#496](https://github.com/home-operations/kritika/issues/496)) ([768b3bb](https://github.com/home-operations/kritika/commit/768b3bbe460967926b7c48718ed189a98f75b9a4))
* **web:** keep the tabs still when the current one changes ([#485](https://github.com/home-operations/kritika/issues/485)) ([311b60e](https://github.com/home-operations/kritika/commit/311b60ece43d51f980b5f1b24b155d1767c86f17))
* **web:** leave no empty cell beside this month's tiles on a phone ([#488](https://github.com/home-operations/kritika/issues/488)) ([3ab6b08](https://github.com/home-operations/kritika/commit/3ab6b0842e7627e5ccecab1c3b14feec0306774c))
* **web:** put whole numbers on the axis of a chart of counts ([#486](https://github.com/home-operations/kritika/issues/486)) ([fa1ed0a](https://github.com/home-operations/kritika/commit/fa1ed0abfeb36fb06250a38dfca749ce4254a2ea))
* **web:** set a repository's row on one line ([#500](https://github.com/home-operations/kritika/issues/500)) ([0acc72a](https://github.com/home-operations/kritika/commit/0acc72a87583d423976e3ee2737733368391ea8f))

## [0.0.22](https://github.com/home-operations/kritika/compare/0.0.21...0.0.22) (2026-10-04)


### Features

* **web:** call the switcher's instance scope "All accounts" ([#470](https://github.com/home-operations/kritika/issues/470)) ([8c4647a](https://github.com/home-operations/kritika/commit/8c4647a17b6f293b223b22427e1a2d132089cbb5))


### Bug Fixes

* **web:** fit the top bar on a phone ([#482](https://github.com/home-operations/kritika/issues/482)) ([0308c6f](https://github.com/home-operations/kritika/commit/0308c6fde148d199a25054b8d8b2a43647b9c955))
* **web:** fit Your settings on a phone ([#478](https://github.com/home-operations/kritika/issues/478)) ([b3a56b4](https://github.com/home-operations/kritika/commit/b3a56b44557538717cfa1d01fe04f07e75973687))
* **web:** honour a viewer's reduced motion setting ([#480](https://github.com/home-operations/kritika/issues/480)) ([75d35a5](https://github.com/home-operations/kritika/commit/75d35a564fc8a129065feb5ff1e8328c142eed9b))
* **web:** pad the repository page's dropped values, and draw its pull requests in one card ([#475](https://github.com/home-operations/kritika/issues/475)) ([5b4945a](https://github.com/home-operations/kritika/commit/5b4945a5dc16ae83482362e4fc74da831c208404))
* **web:** put the queue and a review's usage on a card, as every other table is ([#476](https://github.com/home-operations/kritika/issues/476)) ([08139d7](https://github.com/home-operations/kritika/commit/08139d7f3ee1182b823d63f3ca490a5eb1660f9b))
* **web:** scroll a long page in one place ([#474](https://github.com/home-operations/kritika/issues/474)) ([81f6ef5](https://github.com/home-operations/kritika/commit/81f6ef5b60c9b6bedff885e00611bbdd4b9447ae))
* **web:** show the spinner while a page waits on the session ([#473](https://github.com/home-operations/kritika/issues/473)) ([3fb7703](https://github.com/home-operations/kritika/commit/3fb77038779d09528bfeabf1325247b7211dec3c))
* **web:** space the category counts and the key hints ([#479](https://github.com/home-operations/kritika/issues/479)) ([5b819b4](https://github.com/home-operations/kritika/commit/5b819b4a5fdb6dcd867e4bfc869834aaa3fb80bf))
* **web:** write a run's phases to the second ([#477](https://github.com/home-operations/kritika/issues/477)) ([0d9bff2](https://github.com/home-operations/kritika/commit/0d9bff23c62af3029fd86c12d218f64a8a623036))


### Code Refactoring

* **web:** drop two style rules nothing uses ([#471](https://github.com/home-operations/kritika/issues/471)) ([d1b21f0](https://github.com/home-operations/kritika/commit/d1b21f0c8345b6c2498f81ec440e12815f41b391))
* **web:** name the text sizes and the corner radii ([#483](https://github.com/home-operations/kritika/issues/483)) ([87d41d9](https://github.com/home-operations/kritika/commit/87d41d95a7261acfc1c6a523ba02607d2d0d1549))


### Build System

* **web:** drop Tailwind, which only supplied the reset ([#481](https://github.com/home-operations/kritika/issues/481)) ([322a04a](https://github.com/home-operations/kritika/commit/322a04a0a5105fa4e444cc17c8c7943f05dca396))

## [0.0.21](https://github.com/home-operations/kritika/compare/0.0.20...0.0.21) (2026-10-04)


### Code Refactoring

* record why a review was skipped where it is decided ([#463](https://github.com/home-operations/kritika/issues/463)) ([254812f](https://github.com/home-operations/kritika/commit/254812ff62ea540494815af04fec079e5c5d7767))
* **store:** define what became of a finding once ([#462](https://github.com/home-operations/kritika/issues/462)) ([6877527](https://github.com/home-operations/kritika/commit/6877527be1f4e83d658be2bbc6fdec4225f09992))
* **web:** build the dialog on Bits UI ([#455](https://github.com/home-operations/kritika/issues/455)) ([92544cb](https://github.com/home-operations/kritika/commit/92544cb07964373f525711c9d86f59cb0a42ee01))
* **web:** build the disclosure and the repository switch on Bits UI ([#459](https://github.com/home-operations/kritika/issues/459)) ([f6b6ab6](https://github.com/home-operations/kritika/commit/f6b6ab6eb7ce6bd76497f04ab814422f86390c0c))
* **web:** build the scope and user menus on Bits UI popovers ([#456](https://github.com/home-operations/kritika/issues/456)) ([9d7b689](https://github.com/home-operations/kritika/commit/9d7b6894e8b2b1a1b4052a3cb764a11be7c1cd70))
* **web:** build the segmented control on Bits UI's radio group ([#458](https://github.com/home-operations/kritika/issues/458)) ([86ee3e7](https://github.com/home-operations/kritika/commit/86ee3e7bb5e59a4ad9ac1bad33779acc1c749363))
* **web:** build the shortcuts help and the palette on Bits UI ([#460](https://github.com/home-operations/kritika/issues/460)) ([88c0a4e](https://github.com/home-operations/kritika/commit/88c0a4e432a37e76956e35c9628079e25cd356ba))
* **web:** build the usage meter on Bits UI ([#461](https://github.com/home-operations/kritika/issues/461)) ([b48c7a7](https://github.com/home-operations/kritika/commit/b48c7a7e5713c9b4e052447b0c398f24abeda504))
* **web:** define a pull request's lifecycle once ([#467](https://github.com/home-operations/kritika/issues/467)) ([49d62fa](https://github.com/home-operations/kritika/commit/49d62fad30e89314ca7eb2fa02805d1437903358))
* **web:** give the modals one base style and the layers a scale ([#465](https://github.com/home-operations/kritika/issues/465)) ([123d3c8](https://github.com/home-operations/kritika/commit/123d3c831c939b064473a4a13312c9bdd68a580b))
* **web:** import Shiki's languages by their package, and name the date helpers ([#466](https://github.com/home-operations/kritika/issues/466)) ([c60adea](https://github.com/home-operations/kritika/commit/c60adea56d42c20fa52f70eca46cb4cf3e0078ac))
* **web:** share what the pages repeated ([#464](https://github.com/home-operations/kritika/issues/464)) ([a19fb74](https://github.com/home-operations/kritika/commit/a19fb74cfe3d006c4677c13fc992d58dd2e12d02))
* **web:** write the bulk action's loop once ([#468](https://github.com/home-operations/kritika/issues/468)) ([c36f1f2](https://github.com/home-operations/kritika/commit/c36f1f2263785f616132b03bb87fd73e2b43200b))

## [0.0.20](https://github.com/home-operations/kritika/compare/0.0.19...0.0.20) (2026-10-04)


### Features

* **web:** add the instance's queue, with the model slots jobs wait on ([#443](https://github.com/home-operations/kritika/issues/443)) ([16158ac](https://github.com/home-operations/kritika/commit/16158acbf50d361e2505561be5f28eb57e9297e0))
* **webapi:** keep a user's own dashboard settings ([#448](https://github.com/home-operations/kritika/issues/448)) ([3c3578f](https://github.com/home-operations/kritika/commit/3c3578f0a47b256bca9abf3d44af301b359538a7))
* **web:** find every account's recent pull requests in the palette ([#445](https://github.com/home-operations/kritika/issues/445)) ([8cf5f3f](https://github.com/home-operations/kritika/commit/8cf5f3fc55082006d54723ac9e86322787537939))
* **web:** give the instance and an account their own tabs ([#440](https://github.com/home-operations/kritika/issues/440)) ([b75ff5c](https://github.com/home-operations/kritika/commit/b75ff5c8afa5febeb3b0844955bb23c389b84f7d))
* **web:** highlight the code a review shows ([#452](https://github.com/home-operations/kritika/issues/452)) ([c7cb805](https://github.com/home-operations/kritika/commit/c7cb805ea174c54a21bd1053281185ab33c58ca8))
* **web:** highlight the diff a review read ([#453](https://github.com/home-operations/kritika/issues/453)) ([7ca9aa0](https://github.com/home-operations/kritika/commit/7ca9aa05a2b8ef4b26366b969130c69c06f2d430))
* **web:** keep a light or dark theme with the user ([#451](https://github.com/home-operations/kritika/issues/451)) ([549772a](https://github.com/home-operations/kritika/commit/549772ab8d973e6d04d19d2941db8f058ea3d4a0))
* **web:** keep the page when switching between accounts and the instance ([#444](https://github.com/home-operations/kritika/issues/444)) ([8d6f6ed](https://github.com/home-operations/kritika/commit/8d6f6ed4c8055d2053f3a344f021d04aa8edeac4))
* **web:** let a user choose their time zone and clock ([#450](https://github.com/home-operations/kritika/issues/450)) ([c41bd11](https://github.com/home-operations/kritika/commit/c41bd11c7cd58df1294ed867a57e42f3b0436570))
* **web:** render the Markdown a review writes ([#454](https://github.com/home-operations/kritika/issues/454)) ([e9cdb3a](https://github.com/home-operations/kritika/commit/e9cdb3acb808858bb4cf2b39c747319ac6703033))
* **web:** show each account's health on the instance overview ([#441](https://github.com/home-operations/kritika/issues/441)) ([0484e84](https://github.com/home-operations/kritika/commit/0484e84e839d967a91356ff80a17dcc1faef8a39))
* **web:** write times on the clock the browser's locale keeps ([#447](https://github.com/home-operations/kritika/issues/447)) ([f39ee21](https://github.com/home-operations/kritika/commit/f39ee2100f51f00da4447f0ee0608fdf057ac60f))

## [0.0.19](https://github.com/home-operations/kritika/compare/0.0.18...0.0.19) (2026-10-04)


### Features

* **web:** count everything that needs attention, on the server ([#432](https://github.com/home-operations/kritika/issues/432)) ([87ef38e](https://github.com/home-operations/kritika/commit/87ef38e817d1c8f3a697810d7dc70ae603a7bbed))
* **web:** link a follow-up's question and its reply on GitHub ([#435](https://github.com/home-operations/kritika/issues/435)) ([65663ea](https://github.com/home-operations/kritika/commit/65663eae6608812b74b66f5d1026293c4c29c06d))
* **web:** list the rules a review checked ([#433](https://github.com/home-operations/kritika/issues/433)) ([9141bac](https://github.com/home-operations/kritika/commit/9141bac08d473f07b7bb8b2dd785ab4cc6b9a32a))
* **web:** say when an account was last polled ([#436](https://github.com/home-operations/kritika/issues/436)) ([02f0cbf](https://github.com/home-operations/kritika/commit/02f0cbf4429b34226a2cb49dae926f70fe395b6a))
* **web:** set names in mono and everything else in the text face ([#438](https://github.com/home-operations/kritika/issues/438)) ([d0942a0](https://github.com/home-operations/kritika/commit/d0942a0737546e9cd44718a0dc54bc1716322cba))


### Bug Fixes

* **worker:** build a forge client once when a caller misses a finished build ([#437](https://github.com/home-operations/kritika/issues/437)) ([faa20ef](https://github.com/home-operations/kritika/commit/faa20ef01859b76bf54efabbf53a896cb9e7bc58))

## [0.0.18](https://github.com/home-operations/kritika/compare/0.0.17...0.0.18) (2026-10-04)


### Features

* **review:** collapse earlier findings and list a finding reported again once ([#416](https://github.com/home-operations/kritika/issues/416)) ([5b19277](https://github.com/home-operations/kritika/commit/5b19277be11a11f1b5e4fea273a83e0168752ec7))
* **web:** filter findings by status with a control, and tell open from dismissed ([#426](https://github.com/home-operations/kritika/issues/426)) ([dbeadf7](https://github.com/home-operations/kritika/commit/dbeadf7bc6d072f2c6e4caa6df3f2c974c8ea3d4))
* **web:** list a table's dates newest first ([#423](https://github.com/home-operations/kritika/issues/423)) ([786e3c1](https://github.com/home-operations/kritika/commit/786e3c1c86bb79eea9bb3a04dc60a39d5f10c23b))
* **web:** move through the findings list by keyboard, and list every shortcut ([#427](https://github.com/home-operations/kritika/issues/427)) ([55b4620](https://github.com/home-operations/kritika/commit/55b462029ec6a8e482758fa5e6273ea8299542bb))
* **web:** open a review at the finding a link names ([#421](https://github.com/home-operations/kritika/issues/421)) ([c0fc4be](https://github.com/home-operations/kritika/commit/c0fc4be394edfffd5e49811267aa50ab22d92f9e))
* **web:** say on a review what became of each finding ([#422](https://github.com/home-operations/kritika/issues/422)) ([151fd8c](https://github.com/home-operations/kritika/commit/151fd8c2b00295dd91c0fd5e2586f9216aab219f))
* **web:** say on a review when its pull request has a newer one ([#428](https://github.com/home-operations/kritika/issues/428)) ([2a2d9be](https://github.com/home-operations/kritika/commit/2a2d9be47df90bff61c0de3c25a47bc28546be20))
* **web:** say when a pull request's automatic reviews are paused ([#418](https://github.com/home-operations/kritika/issues/418)) ([1cf2349](https://github.com/home-operations/kritika/commit/1cf23499d7ef123ec556a8685696b5565bf59842))
* **web:** say why a review was skipped, whoever decided it ([#420](https://github.com/home-operations/kritika/issues/420)) ([e155914](https://github.com/home-operations/kritika/commit/e155914d1970cc527a5d896c03dd0d69c86ec0cf))
* **web:** write dates and times one way across the dashboard ([#431](https://github.com/home-operations/kritika/issues/431)) ([3cc05e5](https://github.com/home-operations/kritika/commit/3cc05e54e2088e24bfc2025bc349883a9919fb93))


### Bug Fixes

* **github:** leave review threads open when the App cannot write contents ([#413](https://github.com/home-operations/kritika/issues/413)) ([e51d4d7](https://github.com/home-operations/kritika/commit/e51d4d7d8ca53a837d8c27f6dedb736968341e9c))
* review nothing of a merged or closed pull request ([#430](https://github.com/home-operations/kritika/issues/430)) ([fd3cba0](https://github.com/home-operations/kritika/commit/fd3cba0c742ea989f625ac49b0a7f265daf758dd))
* **web:** keep the repository list's selection, and name what it shows ([#424](https://github.com/home-operations/kritika/issues/424)) ([b598ad0](https://github.com/home-operations/kritika/commit/b598ad0556fdff68a9eff5dcf4b025d6f1a4207f))
* **web:** name the spend table's group and its usage with none ([#429](https://github.com/home-operations/kritika/issues/429)) ([26520f5](https://github.com/home-operations/kritika/commit/26520f525a99c407b22da0ea1cd21cc18eeb6682))
* **web:** offer no reindex of a repository that is off, and link its pull requests ([#425](https://github.com/home-operations/kritika/issues/425)) ([146944c](https://github.com/home-operations/kritika/commit/146944c300ac237523cf580d307255e5ae01f70c))
* **web:** say how far off a time still to come is ([#417](https://github.com/home-operations/kritika/issues/417)) ([0880d6e](https://github.com/home-operations/kritika/commit/0880d6e930558cfbca59e4d6f0610946f8e2f860))

## [0.0.17](https://github.com/home-operations/kritika/compare/0.0.16...0.0.17) (2026-10-03)


### Features

* **webapi:** name the cause of a job's last error when GitHub did not answer ([#406](https://github.com/home-operations/kritika/issues/406)) ([8f2cad2](https://github.com/home-operations/kritika/commit/8f2cad20492542eea35b3d8bb30f801c08792b12))
* **webapi:** say what the queued job is doing when a re-run is refused ([#409](https://github.com/home-operations/kritika/issues/409)) ([238b42b](https://github.com/home-operations/kritika/commit/238b42b8f61688e5f216e2f836637106c69f1760))
* **web:** count a pull request's completed reviews and their cost in the list ([#410](https://github.com/home-operations/kritika/issues/410)) ([c556214](https://github.com/home-operations/kritika/commit/c556214bec8901c79fd884a0804bd39bb1555774))
* **web:** show a pull request's unfinished review job on its page ([#407](https://github.com/home-operations/kritika/issues/407)) ([da6047c](https://github.com/home-operations/kritika/commit/da6047c89b59ac0caf856387d04b4be19ecfb790))

## [0.0.16](https://github.com/home-operations/kritika/compare/0.0.15...0.0.16) (2026-10-03)


### Bug Fixes

* **deps:** update pending dependencies and keep a provider's reason in OpenAI errors ([#404](https://github.com/home-operations/kritika/issues/404)) ([c138897](https://github.com/home-operations/kritika/commit/c138897ee90e83b022add3e57fbc603746764792))

## [0.0.15](https://github.com/home-operations/kritika/compare/0.0.14...0.0.15) (2026-10-03)


### Bug Fixes

* **ingest:** finish a webhook's dispatch when its request ends first ([#399](https://github.com/home-operations/kritika/issues/399)) ([db421bb](https://github.com/home-operations/kritika/commit/db421bb5872006714377610b32c2d8385ca0557c))
* **webapi:** end an event stream when its session no longer stands ([#402](https://github.com/home-operations/kritika/issues/402)) ([0ee01ca](https://github.com/home-operations/kritika/commit/0ee01caa7bfbf7cf0e27f26bee9fd534e5402f01))
* **worker:** replace the pending status of a review that will not run again ([#400](https://github.com/home-operations/kritika/issues/400)) ([e0599b4](https://github.com/home-operations/kritika/commit/e0599b4aad9424a9e43e877e711dceba5cf6ea57))

## [0.0.14](https://github.com/home-operations/kritika/compare/0.0.13...0.0.14) (2026-10-03)


### Bug Fixes

* **agent:** refuse run tool arguments that leak the token or run other programs ([#392](https://github.com/home-operations/kritika/issues/392)) ([66753c1](https://github.com/home-operations/kritika/commit/66753c1be9ac5585b544063437a153fc320b97a0))
* **agent:** wait out a provider outage and bound review job attempts ([#386](https://github.com/home-operations/kritika/issues/386)) ([00ba480](https://github.com/home-operations/kritika/commit/00ba480ac67605be44f72923ed3ae91df98163d5))
* **executor:** give up a runner pod that never starts ([#388](https://github.com/home-operations/kritika/issues/388)) ([47afe36](https://github.com/home-operations/kritika/commit/47afe36d0353e0b1f90f5b67a1dea0d2f23d3d43))
* **gateway:** time out a stalled model request and cap Retry-After ([#385](https://github.com/home-operations/kritika/issues/385)) ([84ac3b4](https://github.com/home-operations/kritika/commit/84ac3b4a88127ffebe25bb24167c8cc1c49cebfb))
* **runner:** bound the diff a context pack keeps and comment listings ([#390](https://github.com/home-operations/kritika/issues/390)) ([cf9a496](https://github.com/home-operations/kritika/commit/cf9a49683f522a945a7e98cd5771bf525fd12db8))
* **serve:** give a cut review time to hand its job back before the kill ([#396](https://github.com/home-operations/kritika/issues/396)) ([28f885f](https://github.com/home-operations/kritika/commit/28f885fbc4ad069969791917d847e5b2acf6f7d0))
* **server:** bound request reads and idle connections on every listener ([#395](https://github.com/home-operations/kritika/issues/395)) ([d5b38a5](https://github.com/home-operations/kritika/commit/d5b38a53b48c3d0dd606f96d26a25fd84646bff6))
* **store:** bound application pool statements and batch retention sweeps ([#389](https://github.com/home-operations/kritika/issues/389)) ([18a8d67](https://github.com/home-operations/kritika/commit/18a8d671899eb7b3cc4360c49fa53db2dce9a7b1))
* **store:** resync consumers when the event listener drops a notification ([#397](https://github.com/home-operations/kritika/issues/397)) ([8046322](https://github.com/home-operations/kritika/commit/8046322b3ee9f135fbc9e20c637365a6afdf1a99))
* **store:** retry a failed leader tenure instead of ending the process ([#393](https://github.com/home-operations/kritika/issues/393)) ([e44f406](https://github.com/home-operations/kritika/commit/e44f4060120981ef31521cca515cdd978b6e2e95))
* **worker:** build the shared forge client apart from its first caller ([#398](https://github.com/home-operations/kritika/issues/398)) ([03c2bc7](https://github.com/home-operations/kritika/commit/03c2bc785cbace18d8d0cb2c05f0bdf91c8a3714))


### Miscellaneous Chores

* **mise:** format and lock build/tools on commit and upgrade its lockfile to revision 3 ([#391](https://github.com/home-operations/kritika/issues/391)) ([df6c97b](https://github.com/home-operations/kritika/commit/df6c97baca579ad506e58a6ad06f68d311e59308))

## [0.0.13](https://github.com/home-operations/kritika/compare/0.0.12...0.0.13) (2026-10-03)


### Features

* **agent:** read_description returns the whole pull request description ([#379](https://github.com/home-operations/kritika/issues/379)) ([7cc9e0b](https://github.com/home-operations/kritika/commit/7cc9e0b9ff7245e5554dd3ac5708522187e82cc7))
* **review:** group the summary comment into Findings and Summary sections ([#382](https://github.com/home-operations/kritika/issues/382)) ([338bf4c](https://github.com/home-operations/kritika/commit/338bf4c0568a6bfac1230b70100db360545a521d))
* **review:** open the summary comment with a one-sentence headline ([#384](https://github.com/home-operations/kritika/issues/384)) ([0afce2e](https://github.com/home-operations/kritika/commit/0afce2ec221bd6f2e9956939d3e08e6d8a43b97f))
* **review:** size the description and issue bodies to the prompt budget ([#378](https://github.com/home-operations/kritika/issues/378)) ([ec503f0](https://github.com/home-operations/kritika/commit/ec503f04d02c557761e001da7de0c96821801e80))
* **review:** title the summary comment and link a re-run badge to the dashboard ([#381](https://github.com/home-operations/kritika/issues/381)) ([e6f6025](https://github.com/home-operations/kritika/commit/e6f6025a11b416d2e56e380444dc5b957dd6deab))
* **worker:** report a running review as pending on the head commit ([#377](https://github.com/home-operations/kritika/issues/377)) ([861c3db](https://github.com/home-operations/kritika/commit/861c3db2a8c3f499c0297369e1c689c137980583))


### Bug Fixes

* **worker:** report a failed review on the head commit ([#375](https://github.com/home-operations/kritika/issues/375)) ([59b6ade](https://github.com/home-operations/kritika/commit/59b6adee03bb85ad8a648be60090d4a6c900b5e4))
* **worker:** report a review ended at admission on the head commit ([#376](https://github.com/home-operations/kritika/issues/376)) ([8f816fd](https://github.com/home-operations/kritika/commit/8f816fdc17709a9410f636c32c1661187e0abd0e))
* **worker:** report an incomplete review as error, not success ([#374](https://github.com/home-operations/kritika/issues/374)) ([20545b4](https://github.com/home-operations/kritika/commit/20545b47724439429e0183cd440fb6b6b5b37028))


### Documentation

* **agents:** point to the org AI Usage Policy instead of restating it ([77702cb](https://github.com/home-operations/kritika/commit/77702cbcbd1ac827b0087ba73436d8a3bdd83254))
* **agents:** update AI usage policy summary ([806c98c](https://github.com/home-operations/kritika/commit/806c98ca56b8519e4e289c0aa39bcc74b24b53a4))


### Miscellaneous Chores

* **github-release:** update release helm-unittest/helm-unittest (v1.2.0 → v1.2.1) ([#373](https://github.com/home-operations/kritika/issues/373)) ([cd03a41](https://github.com/home-operations/kritika/commit/cd03a4117f83aca7e2a7a470fa5a8e3b3dff5358))
* **mise:** update tool aqua:astral-sh/uv (0.12.20 → 0.12.21) ([#368](https://github.com/home-operations/kritika/issues/368)) ([ccd45b6](https://github.com/home-operations/kritika/commit/ccd45b6b657bd188a87fce008615b930afd0f065))
* **mise:** upgrade lockfile to format revision 3 ([4338054](https://github.com/home-operations/kritika/commit/433805494bdbb5a21f0e41386589c0841c60f691))

## [0.0.12](https://github.com/home-operations/kritika/compare/0.0.11...0.0.12) (2026-10-02)


### Bug Fixes

* **review:** redirect the GitHub references the model writes ([#366](https://github.com/home-operations/kritika/issues/366)) ([ea248dc](https://github.com/home-operations/kritika/commit/ea248dc6406c3c70b6aed6dae31b463128c0a1ca))

## [0.0.11](https://github.com/home-operations/kritika/compare/0.0.10...0.0.11) (2026-10-02)


### Features

* **webhook:** parse review thread resolution events ([#359](https://github.com/home-operations/kritika/issues/359)) ([f41e866](https://github.com/home-operations/kritika/commit/f41e866ef4fa86a4f1bd94d659c10624d305e8de))
* **worker:** dismiss a finding when its thread is resolved ([#360](https://github.com/home-operations/kritika/issues/360)) ([e0b76d9](https://github.com/home-operations/kritika/commit/e0b76d9965de78ab6b4f72beaa8c8e113c9f6df4))


### Documentation

* subscribe the App to review thread events ([#362](https://github.com/home-operations/kritika/issues/362)) ([6e4b9bb](https://github.com/home-operations/kritika/commit/6e4b9bb41eaabc6c028a098a72c59a372d584bcc))

## [0.0.10](https://github.com/home-operations/kritika/compare/0.0.9...0.0.10) (2026-10-02)


### Features

* **go:** update module github.com/openai/openai-go/v3 (v3.66.0 → v3.67.0) ([#353](https://github.com/home-operations/kritika/issues/353)) ([7698dc7](https://github.com/home-operations/kritika/commit/7698dc7b1385c47a9fd7924bac29f861497fa1e9))
* **go:** update module github.com/openai/openai-go/v3 (v3.67.0 → v3.68.0) ([#358](https://github.com/home-operations/kritika/issues/358)) ([0b3d749](https://github.com/home-operations/kritika/commit/0b3d749b71297efde5198ff287efef0afd763a29))


### Bug Fixes

* **store:** bind the follow-up comment id filter as bigint ([#350](https://github.com/home-operations/kritika/issues/350)) ([aeb5b58](https://github.com/home-operations/kritika/commit/aeb5b5829717b2be72fc6d254c19b57667627a93))
* treat pull numbers outside int4 as not found ([#351](https://github.com/home-operations/kritika/issues/351)) ([76af367](https://github.com/home-operations/kritika/commit/76af36757098d40f55a8bdba9d22756acad6311e))


### Build System

* **tools:** assemble the runner tools image from pinned release builds on distroless ([#356](https://github.com/home-operations/kritika/issues/356)) ([150ebc7](https://github.com/home-operations/kritika/commit/150ebc74f28acdbfe04a58925e8c9cf0be3da009))

## [0.0.9](https://github.com/home-operations/kritika/compare/0.0.8...0.0.9) (2026-10-02)


### Features

* **chart:** ship a Grafana dashboard ([#347](https://github.com/home-operations/kritika/issues/347)) ([895ae6f](https://github.com/home-operations/kritika/commit/895ae6f5dcb631921cca6fbf039a3698566c0b11))


### Miscellaneous Chores

* **mise:** update tool lefthook (2.1.14 → 2.1.15) ([#346](https://github.com/home-operations/kritika/issues/346)) ([fd986a0](https://github.com/home-operations/kritika/commit/fd986a06d3af3e100f835abba41e93d089c57652))

## [0.0.8](https://github.com/home-operations/kritika/compare/0.0.7...0.0.8) (2026-10-02)


### Features

* **chart:** alert on no leader, two leaders, a refused configuration and drift ([#340](https://github.com/home-operations/kritika/issues/340)) ([919bcb1](https://github.com/home-operations/kritika/commit/919bcb1fe6d61440749f009b7d6ce52ededb3fdd))


### Bug Fixes

* **store:** bound the leader's lock attempt and pings, and the pools' idle pings ([#343](https://github.com/home-operations/kritika/issues/343)) ([9b67a96](https://github.com/home-operations/kritika/commit/9b67a96dac79ef4b0e37a7b1b22a546c8666f0d7))


### Continuous Integration

* boot kritika serve in kind against CloudNativePG ([#341](https://github.com/home-operations/kritika/issues/341)) ([c5bb18e](https://github.com/home-operations/kritika/commit/c5bb18e5459fe33911e58ac88c4b7d30b3acfbc4))


### Miscellaneous Chores

* **deps:** bump River to v0.48.0 ([#344](https://github.com/home-operations/kritika/issues/344)) ([d8d3239](https://github.com/home-operations/kritika/commit/d8d3239aac252eab663e0d3fdb86b4e48d0099f6))

## [0.0.7](https://github.com/home-operations/kritika/compare/0.0.6...0.0.7) (2026-10-02)


### Bug Fixes

* **metrics:** report every pool from one collector, so an owner DSN starts ([#338](https://github.com/home-operations/kritika/issues/338)) ([c24275d](https://github.com/home-operations/kritika/commit/c24275d5cfdab35481918bae733cad086a8b99aa))

## [0.0.6](https://github.com/home-operations/kritika/compare/0.0.5...0.0.6) (2026-10-02)


### Features

* **chart:** run one replica by default ([#319](https://github.com/home-operations/kritika/issues/319)) ([d2acd42](https://github.com/home-operations/kritika/commit/d2acd4285a0a62e21aae6cb6d2113d7176db611f))
* **metrics:** report each connection pool's live state ([#337](https://github.com/home-operations/kritika/issues/337)) ([513a56c](https://github.com/home-operations/kritika/commit/513a56c5ba703f8aac1ff0fad885a422cacf2636))
* **model:** an opencode provider type ([#327](https://github.com/home-operations/kritika/issues/327)) ([97ba016](https://github.com/home-operations/kritika/commit/97ba016e22945522d87a9259f19c7ca365d415cd))
* **model:** carry the conversation on a step ([#325](https://github.com/home-operations/kritika/issues/325)) ([ce75006](https://github.com/home-operations/kritika/commit/ce75006ba279d317c2e24f9cefc173f1262c1820))
* **model:** identify kritika in the user agent ([#324](https://github.com/home-operations/kritika/issues/324)) ([bd56313](https://github.com/home-operations/kritika/commit/bd56313c5800b782c42bf606e98377cff11bb147))


### Bug Fixes

* **forge:** build a forge client outside the cache lock, and bound the wait for GitHub's answer ([#333](https://github.com/home-operations/kritika/issues/333)) ([aed66d2](https://github.com/home-operations/kritika/commit/aed66d2da83ee044508a856aa8bc0e05707545d8))
* **forge:** report the commit status as "Kritika / Review" ([#323](https://github.com/home-operations/kritika/issues/323)) ([5d10839](https://github.com/home-operations/kritika/commit/5d108394ac22ed393f80f9a58721e44a51fe1e10))
* **model:** send each provider request once, so retries are the gateway's alone ([#334](https://github.com/home-operations/kritika/issues/334)) ([0652797](https://github.com/home-operations/kritika/commit/065279781b218e7150d0fd70848544ea2ac6dc40))
* **serve:** ready only once the database answers and webhooks are served ([#330](https://github.com/home-operations/kritika/issues/330)) ([d3f8865](https://github.com/home-operations/kritika/commit/d3f8865fa4b6957cb0f71021b8537db5f1147f0a))
* **store:** keep trying for the leader lock when the database does not answer ([#329](https://github.com/home-operations/kritika/issues/329)) ([0de532c](https://github.com/home-operations/kritika/commit/0de532ce5361c16ce403605fe95451bce1d98f75))
* **worker:** leave one embed slot to similar-code lookups while indexing ([#332](https://github.com/home-operations/kritika/issues/332)) ([cd88ea4](https://github.com/home-operations/kritika/commit/cd88ea486b0db511278447fb5cd7f3b87452708e))


### Performance Improvements

* **poller:** share one App per connection and give a poll its interval ([#335](https://github.com/home-operations/kritika/issues/335)) ([9fb9724](https://github.com/home-operations/kritika/commit/9fb97244e0f870d14126cbf5e65550af3abe666f))
* **store:** index River's index jobs by repository for the onboarding feeder ([#336](https://github.com/home-operations/kritika/issues/336)) ([2d12082](https://github.com/home-operations/kritika/commit/2d120828920aaa46cc49b6886c889b808610d261))


### Documentation

* **chart:** describe the one-replica default accurately ([#321](https://github.com/home-operations/kritika/issues/321)) ([3fe317b](https://github.com/home-operations/kritika/commit/3fe317bf6cad3d87dddfee128be3cf5eedbb24a7))
* **configuration:** describe the opencode provider type ([#328](https://github.com/home-operations/kritika/issues/328)) ([914bb3b](https://github.com/home-operations/kritika/commit/914bb3b469e472e8693e8aaad1d084381ca530ae))

## [0.0.5](https://github.com/home-operations/kritika/compare/0.0.4...0.0.5) (2026-10-02)


### Features

* **config:** widen the default ignore globs ([#316](https://github.com/home-operations/kritika/issues/316)) ([e2d4976](https://github.com/home-operations/kritika/commit/e2d4976a4aff280cc00e850f551ffc2f6d5985f0))


### Bug Fixes

* **chart:** keep schema fences out of the README descriptions ([#317](https://github.com/home-operations/kritika/issues/317)) ([56cecff](https://github.com/home-operations/kritika/commit/56cecff6828b4afbce6f086f662128cb4edbc241))


### Documentation

* cover Secret rotation and runner resources ([#312](https://github.com/home-operations/kritika/issues/312)) ([34c7358](https://github.com/home-operations/kritika/commit/34c7358713b71839e2bcdb60b962aaeca27c1d0e))
* **database:** mount VectorChord as an image volume extension ([#311](https://github.com/home-operations/kritika/issues/311)) ([f04f791](https://github.com/home-operations/kritika/commit/f04f7910e0682f51f9d019b4da852ae0ff73c0bd))


### Miscellaneous Chores

* **mise:** update tool yq (4.53.6 → 4.54.1) ([#315](https://github.com/home-operations/kritika/issues/315)) ([a964c63](https://github.com/home-operations/kritika/commit/a964c63e763e67cd3d939efb69f49d5f859ba259))

## [0.0.4](https://github.com/home-operations/kritika/compare/0.0.3...0.0.4) (2026-10-02)


### ⚠ BREAKING CHANGES

* **chart:** give runner Jobs their own image block, defaulting to the -tools image ([#309](https://github.com/home-operations/kritika/issues/309))

### Features

* **chart:** give runner Jobs their own image block, defaulting to the -tools image ([#309](https://github.com/home-operations/kritika/issues/309)) ([01d7924](https://github.com/home-operations/kritika/commit/01d7924c0179a336e1d0d23ece89272c42e615ed))

## [0.0.3](https://github.com/home-operations/kritika/compare/0.0.2...0.0.3) (2026-10-02)


### Bug Fixes

* **worker:** post a fresh summary when the sticky comment was deleted ([#305](https://github.com/home-operations/kritika/issues/305)) ([ba748a0](https://github.com/home-operations/kritika/commit/ba748a0e244f60eec10b63bded38bc5a2ae0e9a8))


### Miscellaneous Chores

* **mise:** update tool aqua:astral-sh/uv (0.12.19 → 0.12.20) ([#306](https://github.com/home-operations/kritika/issues/306)) ([f8d7a11](https://github.com/home-operations/kritika/commit/f8d7a1198430032dc00ba3ef57925196850b21f2))

## [0.0.2](https://github.com/home-operations/kritika/compare/0.0.1...0.0.2) (2026-10-01)


### Bug Fixes

* **release:** install cosign so the Helm job signs the chart ([#303](https://github.com/home-operations/kritika/issues/303)) ([edcd833](https://github.com/home-operations/kritika/commit/edcd83301bd6392c669d4e7af4e6d2af157404d9))


### Miscellaneous Chores

* **github-action:** update action jdx/mise-action (v4.3.0 → v5.0.0) ([#302](https://github.com/home-operations/kritika/issues/302)) ([84eb3dd](https://github.com/home-operations/kritika/commit/84eb3ddc1cfb2d0e31df7cc1a389825f7506259d))
* **github-release:** update release helm-unittest/helm-unittest (v1.1.2 → v1.2.0) ([#301](https://github.com/home-operations/kritika/issues/301)) ([d64a2a8](https://github.com/home-operations/kritika/commit/d64a2a80b93b426b1543a3ebf6efab73f05f829c))

## 0.0.1 (2026-10-01)


### ⚠ BREAKING CHANGES

* **chart:** key every KRITIKA_* variable under config as its camelCased name ([#294](https://github.com/home-operations/kritika/issues/294))
* **chart:** key how kritika runs under config, the file under configFile ([#292](https://github.com/home-operations/kritika/issues/292))
* **config:** key the configuration file's apps by name ([#283](https://github.com/home-operations/kritika/issues/283))
* **chart:** set every KRITIKA_* variable through env, the configuration file as config ([#281](https://github.com/home-operations/kritika/issues/281))
* rename kritik to kritika ([#262](https://github.com/home-operations/kritika/issues/262))
* **store:** flatten the schema into one migration ([#259](https://github.com/home-operations/kritika/issues/259))
* the gateway is its own package, and the API serves the configuration's types ([#249](https://github.com/home-operations/kritika/issues/249))
* the runner owns the review, the agent searches the index, and the configuration loads once ([#247](https://github.com/home-operations/kritika/issues/247))
* **helm:** the gateway is always on, and serve requires it ([#243](https://github.com/home-operations/kritika/issues/243))
* **review:** every review runs agentic ([#242](https://github.com/home-operations/kritika/issues/242))
* webhooks and the dashboard on one port ([#220](https://github.com/home-operations/kritika/issues/220))
* one server process, kritik serve and kritik run ([#219](https://github.com/home-operations/kritika/issues/219))
* secrets come from the environment only ([#211](https://github.com/home-operations/kritika/issues/211))
* read the configuration once, at startup ([#210](https://github.com/home-operations/kritika/issues/210))
* CEL keys end in Expr ([#201](https://github.com/home-operations/kritika/issues/201))
* lay the configuration file out as apps, repositories and accounts ([#200](https://github.com/home-operations/kritika/issues/200))
* one set of review keys, and allow goes ([#199](https://github.com/home-operations/kritika/issues/199))
* ignore also skips, and skip.onlyPaths goes ([#198](https://github.com/home-operations/kritika/issues/198))
* a rule may be a file, and review.instructions goes ([#197](https://github.com/home-operations/kritika/issues/197))
* one feedback level instead of thoroughness and minSeverity ([#195](https://github.com/home-operations/kritika/issues/195))
* keep the whole configuration in the file ([#188](https://github.com/home-operations/kritika/issues/188))
* **ui:** show the configuration read-only, with a setup checklist ([#187](https://github.com/home-operations/kritika/issues/187))
* review in agentic mode unless a layer says otherwise ([#182](https://github.com/home-operations/kritika/issues/182))
* drop per-account roles and rename operator to admin ([#132](https://github.com/home-operations/kritika/issues/132))
* **store:** key repository names case-insensitively ([#131](https://github.com/home-operations/kritika/issues/131))
* **store:** drop write-only columns, redundant indexes and excess grants ([#121](https://github.com/home-operations/kritika/issues/121))
* drop the per-field policy from the account configuration ([#113](https://github.com/home-operations/kritika/issues/113))
* **chart:** serve kritik at one URL and configure sign-in from values ([#112](https://github.com/home-operations/kritika/issues/112))
* set the embedder in the instance configuration ([#107](https://github.com/home-operations/kritika/issues/107))
* keep the instance configuration in Postgres, keyed by forge account ([#106](https://github.com/home-operations/kritika/issues/106))
* rename tenants to accounts ([#105](https://github.com/home-operations/kritika/issues/105))
* rename installations to connections ([#104](https://github.com/home-operations/kritika/issues/104))
* squash the migrations and call the people who sign in users ([#103](https://github.com/home-operations/kritika/issues/103))
* sign in with a local admin, OIDC or GitHub under a role mapping ([#102](https://github.com/home-operations/kritika/issues/102))
* serve GitHub through a GitHub App alone ([#100](https://github.com/home-operations/kritika/issues/100))
* **config:** an installation serves a list of accounts ([#86](https://github.com/home-operations/kritika/issues/86))
* **gateway:** the worker is the model gateway; no provider key enters a runner ([#27](https://github.com/home-operations/kritika/issues/27))
* **egress:** runner pods leave the cluster only through the worker gateway ([#24](https://github.com/home-operations/kritika/issues/24))
* **store:** flatten the migrations into one schema ([#22](https://github.com/home-operations/kritika/issues/22))
* **store:** index embeddings with VectorChord ([#21](https://github.com/home-operations/kritika/issues/21))
* **review:** render comments with text/template and sprout ([#17](https://github.com/home-operations/kritika/issues/17))
* forgejo forge, agentic reviews, in-repo config and templated output ([#15](https://github.com/home-operations/kritika/issues/15))

### Features

* a rule applies only to pull requests its whenExpr matches ([#202](https://github.com/home-operations/kritika/issues/202)) ([28905d9](https://github.com/home-operations/kritika/commit/28905d99ba0b6d852fc75a0a19b99f7eadfd0ac9))
* a rule may be a file, and review.instructions goes ([#197](https://github.com/home-operations/kritika/issues/197)) ([0c37bc4](https://github.com/home-operations/kritika/commit/0c37bc4f1640ce9b49ffcc6e3038fb715287d8ae))
* add the setup wizard's API and test keys before saving them ([#110](https://github.com/home-operations/kritika/issues/110)) ([71580b9](https://github.com/home-operations/kritika/commit/71580b9bb7293e3fb5d4f860401bf513bcdf9f8f))
* **agent:** run allowlisted commands over a checkout of the head ([#26](https://github.com/home-operations/kritika/issues/26)) ([e5d3935](https://github.com/home-operations/kritika/commit/e5d3935b4d408459f381f14f3d35d176005f0220))
* **agent:** say what rg and fd are for ([#215](https://github.com/home-operations/kritika/issues/215)) ([8fa715b](https://github.com/home-operations/kritika/commit/8fa715b9748a4f6381facc676b39a263360e3026))
* CEL keys end in Expr ([#201](https://github.com/home-operations/kritika/issues/201)) ([9e974d2](https://github.com/home-operations/kritika/commit/9e974d2d7f9085bf75adf49694b4e1b99c5076f2))
* **chart:** key every KRITIKA_* variable under config as its camelCased name ([#294](https://github.com/home-operations/kritika/issues/294)) ([8810c0a](https://github.com/home-operations/kritika/commit/8810c0ab239bf19273536d95662687bc82c4b9f0))
* **chart:** key how kritika runs under config, the file under configFile ([#292](https://github.com/home-operations/kritika/issues/292)) ([8e21271](https://github.com/home-operations/kritika/commit/8e21271695feb84840868a2d308497be892d0a9e))
* **chart:** run two replicas with a PodDisruptionBudget by default ([#212](https://github.com/home-operations/kritika/issues/212)) ([0eedda7](https://github.com/home-operations/kritika/commit/0eedda780406b572fc6add4bd797e0745a63d20c))
* **chart:** serve kritik at one URL and configure sign-in from values ([#112](https://github.com/home-operations/kritika/issues/112)) ([917474c](https://github.com/home-operations/kritika/commit/917474c0508c6ec41780929e2647ed945cef7a03))
* **chart:** set every KRITIKA_* variable through env, the configuration file as config ([#281](https://github.com/home-operations/kritika/issues/281)) ([230c088](https://github.com/home-operations/kritika/commit/230c088635bf141760c43b6afaba5a592a73c8eb))
* **config:** an installation serves a list of accounts ([#86](https://github.com/home-operations/kritika/issues/86)) ([dc6f9a7](https://github.com/home-operations/kritika/commit/dc6f9a7ebec3df931cc8ddf1202cc92c705f9fd5))
* **config:** build each database connection from host and role credentials, a URI remaining an option ([#278](https://github.com/home-operations/kritika/issues/278)) ([2711223](https://github.com/home-operations/kritika/commit/2711223f1be5d2ce4421400d3e94cb277d3223f3))
* **config:** key the configuration file's apps by name ([#283](https://github.com/home-operations/kritika/issues/283)) ([eb07209](https://github.com/home-operations/kritika/commit/eb072096c64808d0f73fcf757531cb2d5e44f184))
* **config:** leave out a file tenant whose names a dashboard tenant holds ([#55](https://github.com/home-operations/kritika/issues/55)) ([00651d3](https://github.com/home-operations/kritika/commit/00651d326cab8bb206654bcb3ace5877cfea088d))
* **config:** let .kritik.yaml choose within the operator's allow bounds ([#58](https://github.com/home-operations/kritika/issues/58)) ([eb01520](https://github.com/home-operations/kritika/commit/eb0152080d62e1121c0442bf31fd13bd6cc25cc2))
* **config:** let every scope set every repository setting, presence winning ([#53](https://github.com/home-operations/kritika/issues/53)) ([b73ddd7](https://github.com/home-operations/kritika/commit/b73ddd7e6655f58e74534f717da0be134be446af))
* **config:** let the defaults and an account turn repositories off ([#145](https://github.com/home-operations/kritika/issues/145)) ([7fd1cf3](https://github.com/home-operations/kritika/commit/7fd1cf31f2b615a596cb7d5dedd0aa6f67dc87d4))
* **config:** move poll, onboarding and runner deadline tuning to the file ([#51](https://github.com/home-operations/kritika/issues/51)) ([a655067](https://github.com/home-operations/kritika/commit/a655067bdf6c26a9088fc4af5572ac330e7e39ce))
* **config:** one policy table for who may write each setting ([#64](https://github.com/home-operations/kritika/issues/64)) ([9f25274](https://github.com/home-operations/kritika/commit/9f25274f4f9bf8bf30ff3bbab56bbbe0e69028d3))
* **config:** set instance defaults in the file, under the dashboard's ([#163](https://github.com/home-operations/kritika/issues/163)) ([d079b90](https://github.com/home-operations/kritika/commit/d079b90b20a08991bd326912377a7306a28a7772))
* **config:** set the review defaults in the file too ([#165](https://github.com/home-operations/kritika/issues/165)) ([dbdfefd](https://github.com/home-operations/kritika/commit/dbdfefdc4a0f60d16c0ac41336e8c9a7e35306a9))
* count the findings that cite each rule ([#191](https://github.com/home-operations/kritika/issues/191)) ([04c449e](https://github.com/home-operations/kritika/commit/04c449e93d7ab758c8b0a342dedc6c5a473dad84))
* **egress:** runner pods leave the cluster only through the worker gateway ([#24](https://github.com/home-operations/kritika/issues/24)) ([416e778](https://github.com/home-operations/kritika/commit/416e778cac256893c17ffb85a67fbfe17cb2ec93))
* **executor:** mount the agent's tools from image volumes ([#54](https://github.com/home-operations/kritika/issues/54)) ([70d6b10](https://github.com/home-operations/kritika/commit/70d6b10cefa55a93b4d162c2b1c7329422c45ee7))
* findings cite the review rules they enforce ([#190](https://github.com/home-operations/kritika/issues/190)) ([e333e0b](https://github.com/home-operations/kritika/commit/e333e0b88110adc36b52b080ea3d6dcb618ccc95))
* **forge:** a GitLab client ([#95](https://github.com/home-operations/kritika/issues/95)) ([339cbe8](https://github.com/home-operations/kritika/commit/339cbe83cf6e1ede3cbf84dddeb8e283d43624e3))
* forgejo forge, agentic reviews, in-repo config and templated output ([#15](https://github.com/home-operations/kritika/issues/15)) ([6f34e8e](https://github.com/home-operations/kritika/commit/6f34e8ed8f9bda52f8aefcb995de3ad6079baecf))
* **forge:** support Gitea installations ([#83](https://github.com/home-operations/kritika/issues/83)) ([73987a0](https://github.com/home-operations/kritika/commit/73987a031b0c32298144b90f666717bef2872577))
* **gateway:** carry a review on with a fallback on another provider ([#298](https://github.com/home-operations/kritika/issues/298)) ([4a66864](https://github.com/home-operations/kritika/commit/4a66864b40adc2c2551c3c1c4d2a5f15582bda40))
* **gateway:** the worker is the model gateway; no provider key enters a runner ([#27](https://github.com/home-operations/kritika/issues/27)) ([38eaaa8](https://github.com/home-operations/kritika/commit/38eaaa80d0b13e604a6e1c505fadfc4ded8078b3))
* **gateway:** try a review's model step again after a transient failure ([#297](https://github.com/home-operations/kritika/issues/297)) ([04f9f9c](https://github.com/home-operations/kritika/commit/04f9f9c54f8113d9989a91bbfe06bd1b973c4c95))
* gh in runners, signed in with the run's read-only token ([#214](https://github.com/home-operations/kritika/issues/214)) ([5b26033](https://github.com/home-operations/kritika/commit/5b26033b486c5374e51022136dcccf6e18944e50))
* **go:** update module github.com/anthropics/anthropic-sdk-go (v1.75.0 → v1.76.0) ([#285](https://github.com/home-operations/kritika/issues/285)) ([d806748](https://github.com/home-operations/kritika/commit/d80674859fe3bec530f729ae9a611b152f9ad025))
* **go:** update module github.com/odvcencio/gotreesitter (v0.54.0 → v0.55.0) ([#11](https://github.com/home-operations/kritika/issues/11)) ([4b797a8](https://github.com/home-operations/kritika/commit/4b797a8cb09e534078ed302b183b78dea6546445))
* **go:** update module github.com/openai/openai-go (v1.12.0 → v3.66.0) ([#5](https://github.com/home-operations/kritika/issues/5)) ([cefd5b2](https://github.com/home-operations/kritika/commit/cefd5b2d36bab6040cf03eff96e0151ae05a9e04))
* **go:** update module golang.org/x/oauth2 (v0.36.0 → v0.37.0) ([#35](https://github.com/home-operations/kritika/issues/35)) ([4cc35e2](https://github.com/home-operations/kritika/commit/4cc35e2fed6af297bd89f9243040b567f63c29ef))
* **helm:** bring the chart in line with the fleet, and fix runner pull secrets ([#238](https://github.com/home-operations/kritika/issues/238)) ([41396c1](https://github.com/home-operations/kritika/commit/41396c16921fac6edff5ec946ff3effaa540991d))
* **helm:** the gateway is always on, and serve requires it ([#243](https://github.com/home-operations/kritika/issues/243)) ([e4bb79f](https://github.com/home-operations/kritika/commit/e4bb79f665f2258b1b6c335e781c263e84c150f1))
* ignore also skips, and skip.onlyPaths goes ([#198](https://github.com/home-operations/kritika/issues/198)) ([a6f9b56](https://github.com/home-operations/kritika/commit/a6f9b5647b2388eb696402dd7fba7446db1e6bd4))
* initial import of the kritik review service ([e27f048](https://github.com/home-operations/kritika/commit/e27f048e560ea3d8235ae816ed350fcb9f87b964))
* keep the instance configuration in Postgres, keyed by forge account ([#106](https://github.com/home-operations/kritika/issues/106)) ([200ab01](https://github.com/home-operations/kritika/commit/200ab015a9cc91990e844302537736f7e277a1ce))
* keep the whole configuration in the file ([#188](https://github.com/home-operations/kritika/issues/188)) ([1cd481f](https://github.com/home-operations/kritika/commit/1cd481fe6cc43383f5c4ec0da282019fb33c2415))
* lay the configuration file out as apps, repositories and accounts ([#200](https://github.com/home-operations/kritika/issues/200)) ([285a804](https://github.com/home-operations/kritika/commit/285a804adee8e8186691f531fce20fba97af16ed))
* leave forks off until turned on, and archived repositories off ([#158](https://github.com/home-operations/kritika/issues/158)) ([514b0b3](https://github.com/home-operations/kritika/commit/514b0b393f958ef305685ff5dd54074df16afa61))
* link each finding to its thread on GitHub ([#177](https://github.com/home-operations/kritika/issues/177)) ([e1eea36](https://github.com/home-operations/kritika/commit/e1eea36e335f47448cc8c816f433a93cee3d714a))
* list a connection's GitHub App installations and remove unserved ones ([#109](https://github.com/home-operations/kritika/issues/109)) ([7fe8087](https://github.com/home-operations/kritika/commit/7fe8087a4a24a38d5aba5dc0e985abbe3aa724b1))
* list an account's findings and whether each was addressed ([#173](https://github.com/home-operations/kritika/issues/173)) ([5592070](https://github.com/home-operations/kritika/commit/55920709247f938f0967a00152974006e9332f2a))
* list the rules an account's reviews read, and where each is named ([#176](https://github.com/home-operations/kritika/issues/176)) ([7ab65f3](https://github.com/home-operations/kritika/commit/7ab65f37eedbef5dfac94ba319e8f44bd09ad6bb))
* meet the first admin with a setup wizard ([#111](https://github.com/home-operations/kritika/issues/111)) ([148ef54](https://github.com/home-operations/kritika/commit/148ef5418a8ef47bec735717b23e7db108c3c9e7))
* **metrics:** kritik_leader, and the pooler mode kritik's connections need ([#221](https://github.com/home-operations/kritika/issues/221)) ([1a8f8b0](https://github.com/home-operations/kritika/commit/1a8f8b0dfcca0ae552b76055cd6cf745c73f2f02))
* **npm:** update dependency oxfmt (0.69.0 → 0.70.0) ([#4](https://github.com/home-operations/kritika/issues/4)) ([f311a0f](https://github.com/home-operations/kritika/commit/f311a0f7851f574c163a333e8512b4884310ff5c))
* **npm:** update dependency oxfmt (0.70.0 → 0.71.0) ([#98](https://github.com/home-operations/kritika/issues/98)) ([70bf6b8](https://github.com/home-operations/kritika/commit/70bf6b870c1aa700e7d31f3cd77d548f67a35e12))
* **npm:** update dependency simple-icons (16.32.0 → 16.33.0) ([#94](https://github.com/home-operations/kritika/issues/94)) ([1c2c088](https://github.com/home-operations/kritika/commit/1c2c088b81fc159ec10963710a0b8ffb99e93249))
* one feedback level instead of thoroughness and minSeverity ([#195](https://github.com/home-operations/kritika/issues/195)) ([c37dcc2](https://github.com/home-operations/kritika/commit/c37dcc24c3ca7b9e501d354e6eb79d5e1c8f2445))
* one server process, kritik serve and kritik run ([#219](https://github.com/home-operations/kritika/issues/219)) ([feecb10](https://github.com/home-operations/kritika/commit/feecb107dd8cdbdf803eab1c4965120d562e65c3))
* one set of review keys, and allow goes ([#199](https://github.com/home-operations/kritika/issues/199)) ([d341875](https://github.com/home-operations/kritika/commit/d341875cacc5d53d2ba242cbaf1fc7e7ae15cffd))
* **poller:** follow a default branch no push webhook reports ([#90](https://github.com/home-operations/kritika/issues/90)) ([048ae0d](https://github.com/home-operations/kritika/commit/048ae0d84c0c771b0d21f1601d63851f121622b1))
* read a repository's AGENTS.md and CLAUDE.md into its reviews ([#193](https://github.com/home-operations/kritika/issues/193)) ([8f251f5](https://github.com/home-operations/kritika/commit/8f251f5bd7afe6c335362996176bb304105edc6e))
* read the configuration once, at startup ([#210](https://github.com/home-operations/kritika/issues/210)) ([90a4af8](https://github.com/home-operations/kritika/commit/90a4af8eb3dddb6fde0c1c752b88ae6d1a0a8373))
* record reactions on findings and time to merge, and report both ([#178](https://github.com/home-operations/kritika/issues/178)) ([f939810](https://github.com/home-operations/kritika/commit/f939810e9f4082662f894b67893cacf23a93deae))
* record whether a repository is archived or a fork ([#157](https://github.com/home-operations/kritika/issues/157)) ([1cd297c](https://github.com/home-operations/kritika/commit/1cd297c2002462bedcbe936ce47a18ea50896439))
* register a connection's GitHub App from a manifest ([#108](https://github.com/home-operations/kritika/issues/108)) ([e9175a3](https://github.com/home-operations/kritika/commit/e9175a3291ca53a75387697434608ded836e8e30))
* register the repositories an App reaches without being asked ([#167](https://github.com/home-operations/kritika/issues/167)) ([e29d92e](https://github.com/home-operations/kritika/commit/e29d92efefbda0f4fa83ffe016e3b39be63a03bd))
* report an account's reviews as analytics against the period before ([#174](https://github.com/home-operations/kritika/issues/174)) ([1661a73](https://github.com/home-operations/kritika/commit/1661a73292ef07be8bb79e6381d8f9932bc2a66f))
* review a fork's pull request when a maintainer asks ([#166](https://github.com/home-operations/kritika/issues/166)) ([09963af](https://github.com/home-operations/kritika/commit/09963afb746d3fb1c5e2f1baf3333599cbf620ba))
* review GitLab merge requests ([#96](https://github.com/home-operations/kritika/issues/96)) ([b0c4b71](https://github.com/home-operations/kritika/commit/b0c4b717e96c7a28b17cfa6203aab4f2aa58f7d3))
* review in agentic mode unless a layer says otherwise ([#182](https://github.com/home-operations/kritika/issues/182)) ([da0e922](https://github.com/home-operations/kritika/commit/da0e9221f5b7016598eb8f05d57b7affdf6fa151))
* review rules written in the configuration ([#183](https://github.com/home-operations/kritika/issues/183)) ([c4c98fa](https://github.com/home-operations/kritika/commit/c4c98fad5e771949259a23a210a8f4635109f166))
* **review:** a thorough review by default, a focused one on request ([#161](https://github.com/home-operations/kritika/issues/161)) ([3a18dfa](https://github.com/home-operations/kritika/commit/3a18dfabc1f93812311dfb17e64dc5ff31fca928))
* **review:** add a severity floor, summary-only output and pr.event ([#59](https://github.com/home-operations/kritika/issues/59)) ([064a852](https://github.com/home-operations/kritika/commit/064a8529041c7a24602fad712951f3e501172723))
* **review:** approve a pull request that has nothing blocking or important ([#236](https://github.com/home-operations/kritika/issues/236)) ([f543ca5](https://github.com/home-operations/kritika/commit/f543ca54050ac5e4d6c0158292bf48583eec635e))
* **review:** dismiss a finding from its thread so later reviews leave it alone ([#273](https://github.com/home-operations/kritika/issues/273)) ([ada35bd](https://github.com/home-operations/kritika/commit/ada35bdfc3871eb25a8f792b41be9a8d63786449))
* **review:** give every finding a category and enforce minimal feedback by it ([#276](https://github.com/home-operations/kritika/issues/276)) ([c1309d7](https://github.com/home-operations/kritika/commit/c1309d72251d652842f07373cdd4808e2093f6b9))
* **review:** name reference files for the reviewer in review.context ([#61](https://github.com/home-operations/kritika/issues/61)) ([e66f42e](https://github.com/home-operations/kritika/commit/e66f42e917cd108ec0dde218aecb5da7aef2e98c))
* **review:** offer a finding's fix as a suggestion and an agent prompt ([#19](https://github.com/home-operations/kritika/issues/19)) ([380bbe0](https://github.com/home-operations/kritika/commit/380bbe0bd06d4fff0b8335b0fd75c69245d83a3d))
* **review:** offer a fix that adds lines as a one-click suggestion ([#296](https://github.com/home-operations/kritika/issues/296)) ([bf5b14e](https://github.com/home-operations/kritika/commit/bf5b14ee764007d58eb80a3a22edb2eb781e2c4e))
* **review:** pause a pull request's automatic reviews on request or after maxAutoReviews ([#274](https://github.com/home-operations/kritika/issues/274)) ([49a194f](https://github.com/home-operations/kritika/commit/49a194ff94c348347bece0428c6cf938be2b604c))
* **review:** read the issues a pull request closes and judge the change against them ([#272](https://github.com/home-operations/kritika/issues/272)) ([23beac9](https://github.com/home-operations/kritika/commit/23beac9dc8f23b0d53e7e530d83d39e4db6f3f5f))
* **review:** render comments with text/template and sprout ([#17](https://github.com/home-operations/kritika/issues/17)) ([15525ad](https://github.com/home-operations/kritika/commit/15525ad5a60957a05e92ff0005c92d4f03e8a7a1))
* **review:** resolve the thread of an earlier finding a later review no longer finds ([#271](https://github.com/home-operations/kritika/issues/271)) ([78cd661](https://github.com/home-operations/kritika/commit/78cd661396f516dc67dcb2bbe533243681fffd44))
* **review:** scope .kritik.yaml instructions to the paths they cover ([#60](https://github.com/home-operations/kritika/issues/60)) ([5690766](https://github.com/home-operations/kritika/commit/5690766bfdd39becafebf18f3e7ed53995cb1bd7))
* **review:** sharpen what the reviewer reports ([#18](https://github.com/home-operations/kritika/issues/18)) ([b4d5520](https://github.com/home-operations/kritika/commit/b4d55207ce93f81a5878226bbc261308a184e8d7))
* **review:** skip an automatic review of a diff over maxChangedLines ([#277](https://github.com/home-operations/kritika/issues/277)) ([b077b2d](https://github.com/home-operations/kritika/commit/b077b2d27d6838e1d3aaae37a7c7120432553944))
* **review:** state the verdict once, link findings to their threads and list earlier and off-diff findings ([#254](https://github.com/home-operations/kritika/issues/254)) ([fb4dd1a](https://github.com/home-operations/kritika/commit/fb4dd1aa50daf3d5a68e0168b269120e590700d6))
* **runner:** run runner Jobs under a RuntimeClass ([#25](https://github.com/home-operations/kritika/issues/25)) ([8674b2a](https://github.com/home-operations/kritika/commit/8674b2ae64b72e4175e0971af870169f0f4a7a39))
* say when an App's webhooks arrive unsigned ([#225](https://github.com/home-operations/kritika/issues/225)) ([38d01d8](https://github.com/home-operations/kritika/commit/38d01d83de96cc2ef74d90c3cc579d93c2ec3444))
* secrets come from the environment only ([#211](https://github.com/home-operations/kritika/issues/211)) ([cc6f609](https://github.com/home-operations/kritika/commit/cc6f6090b637f8bdb8e43f5b2ea0c477f0c44e87))
* serve before the database, check submit_review in the loop, and make the similar-code floor configurable ([#250](https://github.com/home-operations/kritika/issues/250)) ([3273599](https://github.com/home-operations/kritika/commit/3273599157ef27ef4aa857d5a461e2683a55f3b5))
* serve GitHub through a GitHub App alone ([#100](https://github.com/home-operations/kritika/issues/100)) ([b1e8ebd](https://github.com/home-operations/kritika/commit/b1e8ebdc6fc01f21d6e60d5eb24cf1231df4331b))
* set the embedder in the instance configuration ([#107](https://github.com/home-operations/kritika/issues/107)) ([0ed690d](https://github.com/home-operations/kritika/commit/0ed690d8803125d1eb564bd1f4815e124f859ab3))
* sign in with a local admin, OIDC or GitHub under a role mapping ([#102](https://github.com/home-operations/kritika/issues/102)) ([6c29166](https://github.com/home-operations/kritika/commit/6c291665b8da0729899119510a5ab25fbc95267f))
* **store:** index embeddings with VectorChord ([#21](https://github.com/home-operations/kritika/issues/21)) ([23de86e](https://github.com/home-operations/kritika/commit/23de86e1d24721fe676fcfa2941dff3aec589069))
* **store:** retain review diffs for a window, and tighten the schema ([#237](https://github.com/home-operations/kritika/issues/237)) ([9748e26](https://github.com/home-operations/kritika/commit/9748e2642cab34042cff8a5056cb32a567370c95))
* **tools:** add jq and yq to the runner tools image ([#253](https://github.com/home-operations/kritika/issues/253)) ([e7a62c5](https://github.com/home-operations/kritika/commit/e7a62c57524f66204c111ab186ed6194fd51918d))
* turn a repository on or off in the dashboard, not the configuration ([#186](https://github.com/home-operations/kritika/issues/186)) ([e7f0b46](https://github.com/home-operations/kritika/commit/e7f0b4635e647a80e002d788cb56cab27519aec8))
* **ui:** keep the pull list's filters in the URL ([#152](https://github.com/home-operations/kritika/issues/152)) ([e9a10fc](https://github.com/home-operations/kritika/commit/e9a10fc664baacbc4474d57057b90ad816140947))
* **ui:** lay settings out as rows, with search and a save bar ([#175](https://github.com/home-operations/kritika/issues/175)) ([0fd2a8e](https://github.com/home-operations/kritika/commit/0fd2a8ef9d826dbc7352e16c87950ea494371706))
* **ui:** lead a pull request's page with its latest review ([#172](https://github.com/home-operations/kritika/issues/172)) ([d8c6cd5](https://github.com/home-operations/kritika/commit/d8c6cd543a8ba29d1f6f9eaf08272b53db83be32))
* **ui:** let the setup wizard pick the repositories it turns on ([#147](https://github.com/home-operations/kritika/issues/147)) ([b8362bc](https://github.com/home-operations/kritika/commit/b8362bc8fd366f7d18181c8122b6428907a4b571))
* **ui:** link the account's failed and capped reviews from its overview ([#154](https://github.com/home-operations/kritika/issues/154)) ([33e05ff](https://github.com/home-operations/kritika/commit/33e05ff653f5835b429bd88fdbe51f537a843799))
* **ui:** list pull requests in a table searched with filter tokens ([#170](https://github.com/home-operations/kritika/issues/170)) ([c940ccd](https://github.com/home-operations/kritika/commit/c940ccd8ec3ae828d8094a975665e28478b0452b))
* **ui:** mark up the dashboard like a proof ([#168](https://github.com/home-operations/kritika/issues/168)) ([b72aeae](https://github.com/home-operations/kritika/commit/b72aeaeb94c2e2756df0ee5587a0787604c72741))
* **ui:** name each page in the browser tab ([#156](https://github.com/home-operations/kritika/issues/156)) ([9fe2c66](https://github.com/home-operations/kritika/commit/9fe2c66c9b62bbcf49eb7c67c81d47043b2018df))
* **ui:** offer to clear the filters that emptied a list ([#153](https://github.com/home-operations/kritika/issues/153)) ([7e87d3d](https://github.com/home-operations/kritika/commit/7e87d3d7b29e1403a1858d5e59311a935554dd94))
* **ui:** organise the dashboard into sections under a top bar ([#169](https://github.com/home-operations/kritika/issues/169)) ([36f173a](https://github.com/home-operations/kritika/commit/36f173a8f37ab9a8446f1e63e5fc69e27779a62e))
* **ui:** re-run several pull requests' reviews at once ([#179](https://github.com/home-operations/kritika/issues/179)) ([ee966e4](https://github.com/home-operations/kritika/commit/ee966e443db04b3636d2e623cd581d24b569cdbb))
* **ui:** show kritik's version at the foot of the sidebar ([#155](https://github.com/home-operations/kritika/issues/155)) ([bc39292](https://github.com/home-operations/kritika/commit/bc3929222065bd939e4d1d4d7a9e8ac034c76deb))
* **ui:** show the configuration read-only, with a setup checklist ([#187](https://github.com/home-operations/kritika/issues/187)) ([f2c5ddf](https://github.com/home-operations/kritika/commit/f2c5ddf405656fb7d6b87127325bdc2cd2afb651))
* **ui:** show the file's instance defaults in the admin console ([#164](https://github.com/home-operations/kritika/issues/164)) ([bd298b7](https://github.com/home-operations/kritika/commit/bd298b7f63557042ecd969e7e837452d21b79e16))
* **ui:** show whether live updates are connected ([#151](https://github.com/home-operations/kritika/issues/151)) ([2be6c9e](https://github.com/home-operations/kritika/commit/2be6c9ee9faf92c2d0e99bc69cb0efe615aa1bf4))
* **ui:** switch repositories on and off, and reindex several at once ([#146](https://github.com/home-operations/kritika/issues/146)) ([85c5746](https://github.com/home-operations/kritika/commit/85c5746b2cbe6a97f50061fbc2825b3605e8bed9))
* web dashboard with sign-in, transcripts and dashboard-managed config ([#30](https://github.com/home-operations/kritika/issues/30)) ([9b8d02b](https://github.com/home-operations/kritika/commit/9b8d02bb6810e0a4d72927b8a60c84e22b950554))
* **web:** ask which installation when a repository name is ambiguous ([#46](https://github.com/home-operations/kritika/issues/46)) ([03c89ed](https://github.com/home-operations/kritika/commit/03c89ed1f898ffd888b3b2ec2430f4ae24ec2678))
* webhooks and the dashboard on one port ([#220](https://github.com/home-operations/kritika/issues/220)) ([d202848](https://github.com/home-operations/kritika/commit/d20284881b0bbf14e7370dff3541316b50313624))
* **webhook:** verify GitLab's signing token ([#97](https://github.com/home-operations/kritika/issues/97)) ([2399390](https://github.com/home-operations/kritika/commit/23993905f257fd6d1454e4c1047bb6b631f368ce))
* **web:** list instance settings and their sources for operators ([#66](https://github.com/home-operations/kritika/issues/66)) ([ffc9185](https://github.com/home-operations/kritika/commit/ffc9185ac1c88d076a108ddee1be9bb269cb180e))
* **web:** move section navigation to a left sidebar ([#49](https://github.com/home-operations/kritika/issues/49)) ([ed71c90](https://github.com/home-operations/kritika/commit/ed71c9075eb01e2e51c8934e7d86e8d684253ee5))
* **web:** show every tenant's statistics on the home page ([#50](https://github.com/home-operations/kritika/issues/50)) ([ba83d63](https://github.com/home-operations/kritika/commit/ba83d63654c8739f857ed6e75bd9c76beb4bd6ce))
* **web:** show what an empty config field inherits, and from where ([#71](https://github.com/home-operations/kritika/issues/71)) ([b28d0ff](https://github.com/home-operations/kritika/commit/b28d0ff8d1a3956ce685ddc7bde9cb0023199ef9))
* **web:** show where each repository setting comes from ([#65](https://github.com/home-operations/kritika/issues/65)) ([1cda26b](https://github.com/home-operations/kritika/commit/1cda26b37c8ce329a21948823684cb911f4c4de0))
* **web:** show whether each installation receives webhooks ([#79](https://github.com/home-operations/kritika/issues/79)) ([b202637](https://github.com/home-operations/kritika/commit/b20263794e7934ed68d69afebea511c80c6998bb))
* **web:** tenants bring their own provider keys ([#89](https://github.com/home-operations/kritika/issues/89)) ([8171456](https://github.com/home-operations/kritika/commit/81714568a9cb2a37d86fc02ca6e6804e34b7f5c7))
* **worker:** read .kritik.yaml before the runner, and settle there ([#56](https://github.com/home-operations/kritika/issues/56)) ([dba0ecb](https://github.com/home-operations/kritika/commit/dba0ecb344f5e33b2c7d42cc40ad8aea02769f9b))
* **worker:** serve similar code to agentic reviews through the gateway ([#240](https://github.com/home-operations/kritika/issues/240)) ([bedd3b8](https://github.com/home-operations/kritika/commit/bedd3b82260aa728f6d70efea8b4713df92a981b))


### Bug Fixes

* **agent:** ask for the submit call instead of forcing it, and stop on a cut-off review ([#252](https://github.com/home-operations/kritika/issues/252)) ([df4c046](https://github.com/home-operations/kritika/commit/df4c046339c68b81fd695b121cafe56d391f1d3a))
* **auth:** keep the failed sign-in tracker at its cap ([#231](https://github.com/home-operations/kritika/issues/231)) ([56d0c3b](https://github.com/home-operations/kritika/commit/56d0c3b6653139abd45efaddbcfadd85f7fcd276))
* **chart:** the install notes describe the configuration file, not the setup wizard ([#206](https://github.com/home-operations/kritika/issues/206)) ([8c11b64](https://github.com/home-operations/kritika/commit/8c11b644874a679475fcab563010c33d8b39467c))
* **chart:** wait for a startup probe before liveness and readiness ([#69](https://github.com/home-operations/kritika/issues/69)) ([d34cb40](https://github.com/home-operations/kritika/commit/d34cb40a02fa9dc860b9b135a8127d51dcec8cfa))
* **configfile:** key repository entries by installation and name ([#45](https://github.com/home-operations/kritika/issues/45)) ([5be2703](https://github.com/home-operations/kritika/commit/5be270375f7476cb73fcac1db8648860c3760310))
* **config:** refuse a reload that leaves the dashboard no way to sign in ([#129](https://github.com/home-operations/kritika/issues/129)) ([eeba64f](https://github.com/home-operations/kritika/commit/eeba64fe6366ba4585b37271fbdbf4ac294b0ae2))
* **config:** refuse gitlab installations until there is a client ([#68](https://github.com/home-operations/kritika/issues/68)) ([c57671d](https://github.com/home-operations/kritika/commit/c57671dbe81296a3231b42cbb70312adfa29e5ce))
* **config:** tighten configuration checks and stop leader duties before stepping down ([#117](https://github.com/home-operations/kritika/issues/117)) ([b87bf04](https://github.com/home-operations/kritika/commit/b87bf04c3b18949ab6c2af3d72acaf0087ea67fd))
* **contextpack:** count hunk lines, keep new files, and stop the scan cleanly ([#114](https://github.com/home-operations/kritika/issues/114)) ([686a582](https://github.com/home-operations/kritika/commit/686a58221950441aa26a577813f8ec513db6bc4a))
* **executor:** retry a failed Job status read instead of abandoning the Job ([#229](https://github.com/home-operations/kritika/issues/229)) ([8b38464](https://github.com/home-operations/kritika/commit/8b384646ce8433cecf6fed4bef78fc0c05511636))
* **forge:** answer a mention in a Forgejo code conversation ([#75](https://github.com/home-operations/kritika/issues/75)) ([930cbe4](https://github.com/home-operations/kritika/commit/930cbe44f194a87e3a5cff7c49a92f61cd332dd6))
* **forge:** count a GitHub pull request with a deleted fork as a fork ([#73](https://github.com/home-operations/kritika/issues/73)) ([80fd902](https://github.com/home-operations/kritika/commit/80fd9022844d61aeafd3550b1e828569678440f6))
* **forge:** cut commit status descriptions by character, not byte ([#67](https://github.com/home-operations/kritika/issues/67)) ([8e5cb9a](https://github.com/home-operations/kritika/commit/8e5cb9a1b5d063f3524567a1cf879237ef4f7ac6))
* **forge:** hand runners a read-only token for their repository ([#213](https://github.com/home-operations/kritika/issues/213)) ([9e2daf0](https://github.com/home-operations/kritika/commit/9e2daf0673cdf79cff0fd4e5fac62546e55c2a39))
* **forge:** return the oldest matching comment on Forgejo, as on GitHub ([#74](https://github.com/home-operations/kritika/issues/74)) ([5030b29](https://github.com/home-operations/kritika/commit/5030b297172e609c3b95bfa422b14acc83e173da))
* **forge:** wait out GitHub rate limits, count them, and poll past a repository the forge refuses ([#265](https://github.com/home-operations/kritika/issues/265)) ([a03f67e](https://github.com/home-operations/kritika/commit/a03f67e9539eeb8027bcee75d36073fd67372fed))
* **gateway:** reserve each step against the run's budget ([#29](https://github.com/home-operations/kritika/issues/29)) ([686aeb0](https://github.com/home-operations/kritika/commit/686aeb05aa14882a17cfd18856170431514c1c90))
* **go:** update module github.com/go-git/go-billy/v5 (v5.9.0 → v5.9.1) ([#2](https://github.com/home-operations/kritika/issues/2)) ([1f9a6b8](https://github.com/home-operations/kritika/commit/1f9a6b8c80f8994cf90f6a1980240fec3a47fa91))
* **go:** update module github.com/go-jose/go-jose/v4 (v4.1.4 → v4.1.5) ([#34](https://github.com/home-operations/kritika/issues/34)) ([054d550](https://github.com/home-operations/kritika/commit/054d55041b49364043abaff74e0fb94220e36273))
* **go:** update module github.com/odvcencio/gotreesitter (v0.55.0 → v0.55.1) ([#63](https://github.com/home-operations/kritika/issues/63)) ([8625892](https://github.com/home-operations/kritika/commit/862589257309b802ad55b0b1b6f6de0275eeac03))
* handle custom GitHub roles, escaped file links, runner resources and model defaults ([#118](https://github.com/home-operations/kritika/issues/118)) ([85575e7](https://github.com/home-operations/kritika/commit/85575e7b844b4bdc253a2500938117221bd61cb8))
* **indexer:** stage only valid UTF-8, and cut text on rune boundaries everywhere ([#123](https://github.com/home-operations/kritika/issues/123)) ([51ac2f0](https://github.com/home-operations/kritika/commit/51ac2f04682d9d52ca049fca9917b98b334fc1cc))
* **ingest:** count installation events as recorded, not enqueued ([#137](https://github.com/home-operations/kritika/issues/137)) ([b817e62](https://github.com/home-operations/kritika/commit/b817e629f2e999991a6b61794e85351e01cde0d8))
* **ingest:** count only GitHub's own deliveries as unsigned ([#227](https://github.com/home-operations/kritika/issues/227)) ([52ebeef](https://github.com/home-operations/kritika/commit/52ebeefb0e5d3ed746eb5c68a8bc69e1871b292b))
* **ingest:** record a pull request's edits, label changes and draft conversions ([#270](https://github.com/home-operations/kritika/issues/270)) ([97447df](https://github.com/home-operations/kritika/commit/97447df3e430b6d3fcdee42429f2cae8d1032be3))
* **ingest:** skip a polled head a review has already seen ([#226](https://github.com/home-operations/kritika/issues/226)) ([176729d](https://github.com/home-operations/kritika/commit/176729df459dd08b474a8af7cec6c3e4350d6825))
* keep the git token out of the Job spec, drop dead leader sessions, cap under the lease ([#13](https://github.com/home-operations/kritika/issues/13)) ([9f3ee1c](https://github.com/home-operations/kritika/commit/9f3ee1cd1ba56009a624472fbdf669af1377daac))
* keep the public listener and gateway open for 5s after a stop signal ([#223](https://github.com/home-operations/kritika/issues/223)) ([d67a6c6](https://github.com/home-operations/kritika/commit/d67a6c6e97ffda6c09036902437206b316860082))
* **metrics:** count model calls under the model that answered ([#222](https://github.com/home-operations/kritika/issues/222)) ([46b9f03](https://github.com/home-operations/kritika/commit/46b9f038a69650214b026625190416ffd0aafb57))
* **model:** ask OpenRouter for automatic prompt caching ([#251](https://github.com/home-operations/kritika/issues/251)) ([04734be](https://github.com/home-operations/kritika/commit/04734be7a70020be9b1e518c04479434cffb6542))
* **poller:** record a first poll's older pull requests as a baseline ([#38](https://github.com/home-operations/kritika/issues/38)) ([196bbb0](https://github.com/home-operations/kritika/commit/196bbb024344c0eb3dddcf71c62e57a93cfca66f))
* rescue the jobs of a dead replica and reap the runner Jobs they left ([#257](https://github.com/home-operations/kritika/issues/257)) ([a6fc949](https://github.com/home-operations/kritika/commit/a6fc94953a364630adcd15f735efb8aec9b0b43a))
* **review:** drop "Reviews never block a merge." from the summary ([#160](https://github.com/home-operations/kritika/issues/160)) ([4686494](https://github.com/home-operations/kritika/commit/46864949a2463cedc3b6668109c69d4e0e16f081))
* **review:** link sources through redirect.github.com ([#207](https://github.com/home-operations/kritika/issues/207)) ([f7fcb36](https://github.com/home-operations/kritika/commit/f7fcb366ad9164c41ac2d29261db0e30bc8b0bb8))
* **review:** no doubled blank line without findings, no "cannot verify" in the take ([#20](https://github.com/home-operations/kritika/issues/20)) ([9cc8709](https://github.com/home-operations/kritika/commit/9cc870929cc9bfcf967a430e5a79047132cb0a28))
* **review:** review the whole pull request on a re-run at the reviewed head ([#282](https://github.com/home-operations/kritika/issues/282)) ([72cee42](https://github.com/home-operations/kritika/commit/72cee4203be7cdfd15b83c2e4d83f9e9462a5e2d))
* **store:** cap the connection pools by default instead of by node CPU count ([#264](https://github.com/home-operations/kritika/issues/264)) ([3abe63f](https://github.com/home-operations/kritika/commit/3abe63fdb634371a6788203ae06d3a1f9f1b459a))
* **store:** expire the indexes of repositories disabled past their grace ([#43](https://github.com/home-operations/kritika/issues/43)) ([1c0ce18](https://github.com/home-operations/kritika/commit/1c0ce18a7626562ead43162fc2ec8f2ecefb8c1a))
* **store:** key repository names case-insensitively ([#131](https://github.com/home-operations/kritika/issues/131)) ([aecf915](https://github.com/home-operations/kritika/commit/aecf9155c99503c5b51e23ae46bc2de87b3c817a))
* **store:** record merged pull requests, restart listen backoff, and hand unlisted repositories back ([#116](https://github.com/home-operations/kritika/issues/116)) ([873beb9](https://github.com/home-operations/kritika/commit/873beb927119af6f65d09ea86e48e53a9075d8cc))
* survive stale deliveries and retried jobs without duplicate reviews or comments ([#256](https://github.com/home-operations/kritika/issues/256)) ([d804c71](https://github.com/home-operations/kritika/commit/d804c71e738a1e2c1f9eadef36efbec27568cedd))
* **ui:** apply the theme before the first paint ([#149](https://github.com/home-operations/kritika/issues/149)) ([f2edb32](https://github.com/home-operations/kritika/commit/f2edb32ee51861b245aede58c96b488ecc624ab1))
* **ui:** center modal dialogs ([#144](https://github.com/home-operations/kritika/issues/144)) ([ad33bbe](https://github.com/home-operations/kritika/commit/ad33bbeeb958abc119ce6e8080c304457b9cd4e0))
* **ui:** keep the pull list's cursor on its pull across live refetches ([#150](https://github.com/home-operations/kritika/issues/150)) ([88ef675](https://github.com/home-operations/kritika/commit/88ef675f60e579957d106b14550202a274a4c7fe))
* **ui:** refresh connections after a save and leave keystrokes in a select alone ([#119](https://github.com/home-operations/kritika/issues/119)) ([7441867](https://github.com/home-operations/kritika/commit/74418676696304ae387b588b3af2cab68bce5b11))
* **webapi:** let the App manifest form post to GitHub ([#143](https://github.com/home-operations/kritika/issues/143)) ([fd7e3d7](https://github.com/home-operations/kritika/commit/fd7e3d77daf360e83f88b5deff4d90ee630593ea))
* **webapi:** re-seal kept secrets under the current dashboard key ([#130](https://github.com/home-operations/kritika/issues/130)) ([9fe377e](https://github.com/home-operations/kritika/commit/9fe377e4fcdb6b04660e75ed3d6c09eb4136e409))
* **webapi:** the embedding row leaves out the provider's endpoint ([#203](https://github.com/home-operations/kritika/issues/203)) ([5efeeb6](https://github.com/home-operations/kritika/commit/5efeeb6d72c9bd98163f2a17a40e216b2876cde3))
* **web:** lay out the settings sub-nav on narrow screens, and tidy ids, keys and stale ignores ([#255](https://github.com/home-operations/kritika/issues/255)) ([4a1c320](https://github.com/home-operations/kritika/commit/4a1c3206434ac137be6dd34844062f0dd0be68e3))
* **worker:** bound jobs above their runner deadline and delete orphaned Jobs ([#12](https://github.com/home-operations/kritika/issues/12)) ([6a40635](https://github.com/home-operations/kritika/commit/6a406358d353d5604971506c0fbfe11e049c52f7))
* **worker:** clear staged chunks when an index job fails ([#42](https://github.com/home-operations/kritika/issues/42)) ([ab40ef8](https://github.com/home-operations/kritika/commit/ab40ef8abf9442ce37a1ddcd911ed9026721606d))
* **worker:** drain jobs on shutdown, and retry the reviews it cuts ([#218](https://github.com/home-operations/kritika/issues/218)) ([754a895](https://github.com/home-operations/kritika/commit/754a8957bd9733738cdc811f2312a15c4bec87c7))
* **worker:** end a retried job's earlier review and size timeouts for .kritik.yaml ([#115](https://github.com/home-operations/kritika/issues/115)) ([1c4d16b](https://github.com/home-operations/kritika/commit/1c4d16bc21c7ea0cfb079edc0e96353c6e0f76b8))
* **worker:** keep no transaction open while an index embeds ([#77](https://github.com/home-operations/kritika/issues/77)) ([c665fe2](https://github.com/home-operations/kritika/commit/c665fe2b21795ba14370a53de32421716c89a092))
* **worker:** log a failed default-branch backfill instead of dropping it ([#233](https://github.com/home-operations/kritika/issues/233)) ([6c9ea16](https://github.com/home-operations/kritika/commit/6c9ea16dbd907ae7af93e9603b283d7564b19aa9))
* **worker:** pace the index queue ([#37](https://github.com/home-operations/kritika/issues/37)) ([bb43a03](https://github.com/home-operations/kritika/commit/bb43a03c5e54cfb644e96fbd5f88047a588a00b0))
* **worker:** snooze a review while every model slot is held ([#33](https://github.com/home-operations/kritika/issues/33)) ([3fe2df7](https://github.com/home-operations/kritika/commit/3fe2df714d64505b3743dc3b5cdd4923b8799ad7))
* **worker:** sweep only index runs River has given up on ([#78](https://github.com/home-operations/kritika/issues/78)) ([f874532](https://github.com/home-operations/kritika/commit/f8745329cec368a4153c91674015937692774f89))


### Performance Improvements

* **forge:** walk a Forgejo pull request's reviews once per mention ([#76](https://github.com/home-operations/kritika/issues/76)) ([b4b83cc](https://github.com/home-operations/kritika/commit/b4b83cc79e069a9d7c497477a66ec177fa6c2c9f))
* **github:** mint an installation's token once per App ([#230](https://github.com/home-operations/kritika/issues/230)) ([da5bf1e](https://github.com/home-operations/kritika/commit/da5bf1e818ee1dad8bdea37bf6b7069600a64173))
* **worker:** skip an unchanged bot rebase before starting its runner ([#32](https://github.com/home-operations/kritika/issues/32)) ([a3f5569](https://github.com/home-operations/kritika/commit/a3f55695c8617acca68be09b9b5a886ddb45f417))


### Code Refactoring

* **agent:** keep the default agent limits in one place ([#138](https://github.com/home-operations/kritika/issues/138)) ([fcfd666](https://github.com/home-operations/kritika/commit/fcfd666ef0ab1833be6987478738e95da8938628))
* apply reuse, simplification and efficiency cleanups across packages ([#234](https://github.com/home-operations/kritika/issues/234)) ([9295cca](https://github.com/home-operations/kritika/commit/9295cca26c91aea5afbdd8a65ffc624d02bbb6ca))
* deduplicate helpers and remove quadratic loops ([#41](https://github.com/home-operations/kritika/issues/41)) ([af1ca71](https://github.com/home-operations/kritika/commit/af1ca7170e4847b68296274b314e6bb2c0909fb0))
* drop per-account roles and rename operator to admin ([#132](https://github.com/home-operations/kritika/issues/132)) ([069bc1b](https://github.com/home-operations/kritika/commit/069bc1b5e6180f3344b97eeeeadc6a1458a80e01))
* drop the per-field policy from the account configuration ([#113](https://github.com/home-operations/kritika/issues/113)) ([41e8d03](https://github.com/home-operations/kritika/commit/41e8d0399c6cc51280759809bb21a97df2da2d4b))
* modernise for Go 1.27, share the worker plumbing, widen unit tests ([#10](https://github.com/home-operations/kritika/issues/10)) ([0374c74](https://github.com/home-operations/kritika/commit/0374c740004bf85db7c443025107bb62eb1f5fc9))
* modernize idioms for Go 1.27 ([#125](https://github.com/home-operations/kritika/issues/125)) ([caffcaa](https://github.com/home-operations/kritika/commit/caffcaa300df80b0aee0479d2489fe2358841290))
* remove dead code, unread fields and unused parameters ([#120](https://github.com/home-operations/kritika/issues/120)) ([a5ee58a](https://github.com/home-operations/kritika/commit/a5ee58a561e1a3fb1b9f6ce35c25ef02581d8312))
* remove wrappers and a one-value enum that only tests kept alive ([#235](https://github.com/home-operations/kritika/issues/235)) ([2961049](https://github.com/home-operations/kritika/commit/29610498ce8b83be0c6942a4bf40575e32fd140d))
* rename installations to connections ([#104](https://github.com/home-operations/kritika/issues/104)) ([93edab9](https://github.com/home-operations/kritika/commit/93edab9e5c19a0f5e0f9877fa615c49b0792306c))
* rename kritik to kritika ([#262](https://github.com/home-operations/kritika/issues/262)) ([ccda488](https://github.com/home-operations/kritika/commit/ccda488b48ae7700bdc10977a942109204169969))
* rename tenants to accounts ([#105](https://github.com/home-operations/kritika/issues/105)) ([e525c7b](https://github.com/home-operations/kritika/commit/e525c7b27b1d020acd7905e919b0f97b6a239976))
* **review:** every review runs agentic ([#242](https://github.com/home-operations/kritika/issues/242)) ([5b12f90](https://github.com/home-operations/kritika/commit/5b12f90a010d9b7a31427a214fdf934c269b5131))
* share duplicated logic ([#124](https://github.com/home-operations/kritika/issues/124)) ([4f79b3c](https://github.com/home-operations/kritika/commit/4f79b3c77eb5c0321a509554b5e6f2a30261d98d))
* squash the migrations and call the people who sign in users ([#103](https://github.com/home-operations/kritika/issues/103)) ([476381a](https://github.com/home-operations/kritika/commit/476381a6548ca1805c55ef5c81fb121fb0bede45))
* **store:** drop write-only columns, redundant indexes and excess grants ([#121](https://github.com/home-operations/kritika/issues/121)) ([bd8eeaf](https://github.com/home-operations/kritika/commit/bd8eeaf2bafab0173dc1fd15d157285e8ccad8aa))
* **store:** flatten the migrations into one schema ([#22](https://github.com/home-operations/kritika/issues/22)) ([f0ce264](https://github.com/home-operations/kritika/commit/f0ce2640c7ec8d1e36a8b6861f8f288964539b6d))
* **store:** flatten the schema into one migration ([#259](https://github.com/home-operations/kritika/issues/259)) ([f4a6976](https://github.com/home-operations/kritika/commit/f4a6976d5392a8fe354151b9749eb8dabf741960))
* **store:** the index generation, ingest and poll SQL move into the store ([#248](https://github.com/home-operations/kritika/issues/248)) ([b4a05d1](https://github.com/home-operations/kritika/commit/b4a05d1c8466095d716fac483a0aeb825c158336))
* the gateway is its own package, and the API serves the configuration's types ([#249](https://github.com/home-operations/kritika/issues/249)) ([2d27b63](https://github.com/home-operations/kritika/commit/2d27b632b6cf4b6e56b11c7af16dd01999545309))
* the runner owns the review, the agent searches the index, and the configuration loads once ([#247](https://github.com/home-operations/kritika/issues/247)) ([845b5b2](https://github.com/home-operations/kritika/commit/845b5b2c6de7e40882e6b461f4934800e798fe9f))
* **ui:** remove dead code and fix stale wording ([#122](https://github.com/home-operations/kritika/issues/122)) ([47decae](https://github.com/home-operations/kritika/commit/47decae8b7deee0e9f85d405dc8212d052c15309))
* **ui:** share the configuration editors' shell and save flow ([#133](https://github.com/home-operations/kritika/issues/133)) ([d391880](https://github.com/home-operations/kritika/commit/d3918803162d26eb00f9048a25a6e55cba59444a))
* **webapi:** serve the admin audit log through the admin adapter ([#232](https://github.com/home-operations/kritika/issues/232)) ([2b99e33](https://github.com/home-operations/kritika/commit/2b99e33dc893f7c534aa5744f9afbd8e61cfb0bc))
* **webhook:** drop verifyHMAC's constant prefix parameter ([#135](https://github.com/home-operations/kritika/issues/135)) ([2941119](https://github.com/home-operations/kritika/commit/29411190ff016b8e403628383ac70cbd1b495c67))
* **worker:** find a GitHub App's installation from each repository's owner ([#85](https://github.com/home-operations/kritika/issues/85)) ([882069f](https://github.com/home-operations/kritika/commit/882069fe61554f1af791053651cd87065dbf4568))
* **worker:** use the store's review statuses ([#136](https://github.com/home-operations/kritika/issues/136)) ([a46f143](https://github.com/home-operations/kritika/commit/a46f1431d7b4aeb8fa8f8b827fee33d43b928a65))


### Reverts

* **config:** build each database connection from a URI again ([#291](https://github.com/home-operations/kritika/issues/291)) ([f2d21e3](https://github.com/home-operations/kritika/commit/f2d21e3c5a07ce39dd93d0b298522af10e7fdea8))


### Documentation

* add CloudNativePG guidance for the database ([#263](https://github.com/home-operations/kritika/issues/263)) ([6525650](https://github.com/home-operations/kritika/commit/65256507ad390309de7cfaf5377bed8b0f87934c))
* **adr:** accept ADR-0010 and ADR-0011 ([#70](https://github.com/home-operations/kritika/issues/70)) ([68a13cd](https://github.com/home-operations/kritika/commit/68a13cdf6064d9b2e46d4e2353cc68a348d93fcb))
* **adr:** add ADR-0010 on configuration layers and precedence ([#44](https://github.com/home-operations/kritika/issues/44)) ([e0e038f](https://github.com/home-operations/kritika/commit/e0e038fcf82cb0b357365056db488ea64782af73))
* **adr:** add Greptile-informed .kritik.yaml content to ADR-0010 ([#48](https://github.com/home-operations/kritika/issues/48)) ([a95bdfa](https://github.com/home-operations/kritika/commit/a95bdfaa13e9b6859c47c2570599d170fdb8dbbc))
* **adr:** ADR-0003, the review is an agent in the runner, the worker its model gateway ([#14](https://github.com/home-operations/kritika/issues/14)) ([c30b386](https://github.com/home-operations/kritika/commit/c30b386ad3d1e3bba31e82481f9a4ae9c3f1b82f))
* **adr:** ADR-0008, allowlisted commands in the agent and egress through the gateway ([#23](https://github.com/home-operations/kritika/issues/23)) ([501b449](https://github.com/home-operations/kritika/commit/501b4498d3a684c68f2c93867f4095fd96ec2c39))
* **adr:** every review is agentic, and the index reaches it through the gateway ([#239](https://github.com/home-operations/kritika/issues/239)) ([5d87208](https://github.com/home-operations/kritika/commit/5d87208958d5e422472cd934b494961429d896c9))
* **adr:** keep the configuration in git and have the dashboard read it ([#185](https://github.com/home-operations/kritika/issues/185)) ([7e9d635](https://github.com/home-operations/kritika/commit/7e9d635724118c0ad74c3f05832a102f2bff0dc1))
* **adr:** one server process, kritik serve and kritik run ([#216](https://github.com/home-operations/kritika/issues/216)) ([fd3fafc](https://github.com/home-operations/kritika/commit/fd3fafccf8eb843c7a28ea0081fae54a3f6b2273))
* **adr:** one shape for the configuration and .kritik.yaml ([#194](https://github.com/home-operations/kritika/issues/194)) ([3302d83](https://github.com/home-operations/kritika/commit/3302d83246a1628b603eab46c0cbd5fa8bd122a6))
* **adr:** propose a hosted instance that customers bring their own model keys to ([#87](https://github.com/home-operations/kritika/issues/87)) ([2ac4e93](https://github.com/home-operations/kritika/commit/2ac4e93004148c598701dd7ba764cb1900c04e6f))
* **adr:** propose a self-hosted instance, set up in the dashboard, that serves GitHub through one App ([#99](https://github.com/home-operations/kritika/issues/99)) ([6ae17a0](https://github.com/home-operations/kritika/commit/6ae17a02f982341b3ede6ac2a3ce855dac7754e5))
* **adr:** propose creating a dashboard GitHub installation's App from a manifest ([#81](https://github.com/home-operations/kritika/issues/81)) ([b9e4439](https://github.com/home-operations/kritika/commit/b9e44395c9860d82de2d759b9ecea82baf547d66))
* **adr:** read the configuration at startup, take secrets from the environment, run two replicas ([#208](https://github.com/home-operations/kritika/issues/208)) ([163252d](https://github.com/home-operations/kritika/commit/163252d73527ec9268e6ccd0a1180a1a221970e9))
* **adr:** renumber the gateway ADR as 0004 amending the agentic mode ([#16](https://github.com/home-operations/kritika/issues/16)) ([ad9f727](https://github.com/home-operations/kritika/commit/ad9f72755ff2af2a09f027bc6e639b2fc067ac2f))
* **chart:** state the bound on concurrent runner pods ([#39](https://github.com/home-operations/kritika/issues/39)) ([03c8f65](https://github.com/home-operations/kritika/commit/03c8f65877de35fb7296bb12d57bd7734daf7ef0))
* **configuration:** how to use a local model ([#204](https://github.com/home-operations/kritika/issues/204)) ([b712797](https://github.com/home-operations/kritika/commit/b71279799bcb39240c8e23d85c5e3ccd94d5cf03))
* connect a forge through one webhook, not one per repository ([#80](https://github.com/home-operations/kritika/issues/80)) ([d943151](https://github.com/home-operations/kritika/commit/d943151576acd2ba397a27246fc496228b1e994a))
* connect a Gitea installation ([#91](https://github.com/home-operations/kritika/issues/91)) ([2d6cba4](https://github.com/home-operations/kritika/commit/2d6cba44283e4a70bdfef299d520f7177b613f09))
* describe the configuration kept in git ([#189](https://github.com/home-operations/kritika/issues/189)) ([d2a63c3](https://github.com/home-operations/kritika/commit/d2a63c3ae33bbadc60e3f7873c747944f8ad6d30))
* fix stale comments, wording and descriptions ([#126](https://github.com/home-operations/kritika/issues/126)) ([31f74a5](https://github.com/home-operations/kritika/commit/31f74a50daf5b1cc9673bcf7ff74aa0167e66782))
* publish a JSON Schema for .kritik.yaml ([#62](https://github.com/home-operations/kritika/issues/62)) ([1c308b5](https://github.com/home-operations/kritika/commit/1c308b5eac0d176891b1b15d80ca630b3082e205))
* publish the docs as a site, like tuppr ([#261](https://github.com/home-operations/kritika/issues/261)) ([e0812f8](https://github.com/home-operations/kritika/commit/e0812f85383d8390094156b092c5f00d8970f06e))
* remove the ADRs and every reference to them ([#258](https://github.com/home-operations/kritika/issues/258)) ([64bdf19](https://github.com/home-operations/kritika/commit/64bdf19bf5b2a3cae36cde34ad4d46276a235f6c))
* slim the README and flag kritik as not production ready ([#40](https://github.com/home-operations/kritika/issues/40)) ([9d9581b](https://github.com/home-operations/kritika/commit/9d9581bf640a3ee2b07deeed318d0286af3c5827))


### Tests

* **gateway:** count only the suite's own gateway tokens ([#28](https://github.com/home-operations/kritika/issues/28)) ([2b7cc81](https://github.com/home-operations/kritika/commit/2b7cc8177a9d207329a14b80c88632649ca9f343))
* **ui:** answer the leave dialog before the test ends ([#127](https://github.com/home-operations/kritika/issues/127)) ([adc26bb](https://github.com/home-operations/kritika/commit/adc26bbfb8ea5c426ce662fd28a58ba087fcfad2))
* **webapi:** count findings by category in the analytics golden ([#288](https://github.com/home-operations/kritika/issues/288)) ([1c78fbf](https://github.com/home-operations/kritika/commit/1c78fbf1963c02c1b4e6ab5673fd6ffed07a8838))
* **worker:** wait for the skipped review's job before re-running it ([#289](https://github.com/home-operations/kritika/issues/289)) ([59539d9](https://github.com/home-operations/kritika/commit/59539d953c764ecc78280c51c059d0386bc1add7))


### Build System

* **chart:** keep the generated README and schema out of the formatter ([#9](https://github.com/home-operations/kritika/issues/9)) ([1e3fd18](https://github.com/home-operations/kritika/commit/1e3fd187d4b4ff0443668ee0ce295c79f966d14f))
* **mise:** lock node without a machine-local compile option ([#31](https://github.com/home-operations/kritika/issues/31)) ([8082243](https://github.com/home-operations/kritika/commit/8082243c20c36b242f694f64578bab88e6c80f99))
* **mise:** stop tracking the lock sidecars ([#8](https://github.com/home-operations/kritika/issues/8)) ([b695f42](https://github.com/home-operations/kritika/commit/b695f4264afaedfd1e39e3c1a7709232362add5b))


### Continuous Integration

* **release:** start at 0.0.1 and bump only the patch ([#300](https://github.com/home-operations/kritika/issues/300)) ([9b2e8d6](https://github.com/home-operations/kritika/commit/9b2e8d64fcd44ff197de28f3a0ec163ff08c1b8b))
* **release:** start the version series at 0.1.0 ([#7](https://github.com/home-operations/kritika/issues/7)) ([120b478](https://github.com/home-operations/kritika/commit/120b4782a46ecc81ac28a567a7d5e0c55a38f169))


### Miscellaneous Chores

* **chart:** take the network policy's Postgres port from database.port ([#280](https://github.com/home-operations/kritika/issues/280)) ([e13d1c1](https://github.com/home-operations/kritika/commit/e13d1c161ec5831518c743edffe5e6f775695eea))
* **lefthook:** keep oxfmt off ignored docs and generated chart files, regenerate the chart docs on commit ([#266](https://github.com/home-operations/kritika/issues/266)) ([4190577](https://github.com/home-operations/kritika/commit/41905778fe6f7e7cc128688df6e92b757cd7dc02))
* **mise:** lock file maintenance tool (mise) ([#246](https://github.com/home-operations/kritika/issues/246)) ([49223c1](https://github.com/home-operations/kritika/commit/49223c1f96947f5e4e2e4e20bf891214c68ca34c))
* **mise:** push dev images to a configurable registry ([#140](https://github.com/home-operations/kritika/issues/140)) ([a31a156](https://github.com/home-operations/kritika/commit/a31a156700c0cfd15acf7fc2b4dbd0f8dd880c6c))
* **mise:** update tool aqua:astral-sh/uv (0.12.16 → 0.12.19) ([#267](https://github.com/home-operations/kritika/issues/267)) ([54a7c6a](https://github.com/home-operations/kritika/commit/54a7c6ac2116e5e6f491acf61ac6cf57378e8ca8))
* **mise:** update tool golangci-lint (2.13.2 → 2.14.0) ([#93](https://github.com/home-operations/kritika/issues/93)) ([ebcf0d2](https://github.com/home-operations/kritika/commit/ebcf0d213779dd57123b08ffaff892aa064513d9))
* **mise:** update tool oxfmt (0.69.0 → 0.70.0) ([#3](https://github.com/home-operations/kritika/issues/3)) ([ec201c5](https://github.com/home-operations/kritika/commit/ec201c50f5dac14efba42d6526a6e776dcec637d))
* **mise:** update tool oxfmt (0.70.0 → 0.71.0) ([#268](https://github.com/home-operations/kritika/issues/268)) ([b56a73f](https://github.com/home-operations/kritika/commit/b56a73fe0ec8b11347b69a7305eeec166b4a561d))
* **store:** drop the config projections nothing reads ([#52](https://github.com/home-operations/kritika/issues/52)) ([51bcb46](https://github.com/home-operations/kritika/commit/51bcb46926811a0276c0bba51ccc71bd52fe9d75))
* **store:** give the Gitea migration its own number ([#92](https://github.com/home-operations/kritika/issues/92)) ([03ce65d](https://github.com/home-operations/kritika/commit/03ce65d6d14bc06494cd9d1a61e5d257bbed7472))
