package imagebuild

// oldFrontendDigests are the sha256 digests (hex) of every manifest list and
// every per-platform manifest behind the docker/dockerfile tags 0.9, 0.9.0,
// 0.9-experimental, 0.9.0-experimental, 1.0, 1.0.0, 1.0.1, 1.0-experimental,
// 1.0.0-experimental, 1.0.1-experimental and 1.0.2-experimental, as Docker
// Hub's registry served them in October 2026 (the tags were last pushed in
// 2019; docker/dockerfile-upstream's tags of those names are the same
// images). A digest decides which image BuildKit pulls whatever tag stands
// beside it, so "docker/dockerfile:1.7@sha256:<one of these>" runs the old
// frontend (CS-IMG-079; verified in a real build).
//
// Method: for each tag, an anonymous token from
// auth.docker.io/token?service=registry.docker.io&scope=repository:docker/dockerfile:pull,
// then GET https://registry-1.docker.io/v2/docker/dockerfile/manifests/<tag>
// with the manifest-list, OCI-index and image-manifest media types in
// Accept; the response's Docker-Content-Digest header (the list) plus every
// .manifests[].digest (the per-platform manifests). The same requests
// against docker/dockerfile-upstream returned the same digests.
//
// It covers only manifests those tags name TODAY. An old manifest no tag
// names any more is not here, which is why classifyFrontend treats every
// unlisted digest as unknown (a warning), never as modern. Regenerate the
// list if those tags ever move.
var oldFrontendDigests = map[string]bool{
	"03606bee94db0823c7b0d10f8e9ce8218601c8c6d9655de0d2d86e1b6b870c4e": true,
	"0ae875f15b184c2e8cb6310d009588ef82302268d790e6497a2dba5a8ef85f16": true,
	"18b6dbc529526d02baf9d7d3d1537d9087414a6678bf2f27fdfaa0c63da7db77": true,
	"1a2123d4013f1a9a50173847d5e81a539d40938c2eac528e5c081cdc3aed1058": true,
	"1de753ef0a5cfd695e1a554063580bfc374ddda35f017a0fe8233a93abb439be": true,
	"2220efe9582e00cd8f6bbee8f4566e34d7f0388c0e10f2b20499cc19ddd140d8": true,
	"26cdad2543f02dbbe743b8fc0afe0ad270398e0e89bfb03245ed754736ab59ee": true,
	"42df958dfd9c19c8b23318906cb9939a4ab8c40d475bfebfa2026198cb59eedb": true,
	"446b05ddf76f416e4b508dd1f8cff1a928e098db27d5c0c795fd700561e82ed8": true,
	"478b837bb9f2115e521fbae8affd413ab00cd42090477d808c0d9dbcf54ca27d": true,
	"4e7704670349cdae408f0114f7e9b48f350932e241bac8d3126631403156f56f": true,
	"52f8f4fe439c76ac2f161809c6a9ff2fb4df40f9eb6867259b6df1efbab56b1b": true,
	"534ec1e82e9a7facc2a934d822af95fca48f2954f85d6fb274a2aa83a584c02f": true,
	"5c5e3aefa1761c51c2f2d0d3c5793fb86611421d4d98526d33bd187a7b8eb8b9": true,
	"5e56e9613aa67c265dd26dd7784306e405091fc0ed0c8999548550fb86211c4e": true,
	"6156840cd820238208c56b4f9e6eef92e68178253b3d55123d128257e63fd55b": true,
	"653ad79c9cd2fe8105290b61bc0f261c6f73a9e9cb1780bdb529c396e04f9655": true,
	"6e733102ce23b4cdd8f91407129165fead00c4cf45e85d293c8d22b896e822b6": true,
	"74f208c03ad66012f800d4efd60889d57e3a4f97e6952c3e99545d376880ad6c": true,
	"7830f51f4697ef92531a316b98284be7a9060146847b935bfb3c759ff1a932f2": true,
	"79dd02d8cfdc6ec2556607eee475cef7ff49ea4c9a7ddbd174ee051ded08f4a8": true,
	"7c4ae36b91c20c51b1aaca354741719ff17abb6bd530294a4e4266a628a5f552": true,
	"81eb992f53aeffcbdd845aabb85dc984a4faa98dd8929f9a7141f32837748006": true,
	"82d4fe8a871793557208e8e51262d7063817ad4d3b7ebeae86d292699aa4b150": true,
	"87b181b4bc7b93312be2ca338e0b4223f4750e4ae301ce5c867218135bdd939d": true,
	"90251afc27ab89c025d205bd3fb9c3e110a653476c26b800ce693de2e6178243": true,
	"92f5351b2fca8f7e2f452aa9aec1c34213cdd2702ca92414eee6466fab21814a": true,
	"9349a3e94e5a58e7ae582e1a3af64f69cdb1a116cf2a299c24fc4d1b22855f0c": true,
	"9f37bbf3e89214574a30356139381d312e2a8d80877a312d31a1446eb2bdfa4a": true,
	"a5f5d515153d1b80711a6f18f6e07fb20c3d3daf880fd7621030bb9ffa3e2620": true,
	"a69558a5414593ec1c871ee50a4a388450294eb1110f126effec2ec0d705fab1": true,
	"aaf1370bcf77e52afd5b19113426b005a79641ffe10e2705d135e1f3403e7a4d": true,
	"b4fae4e8b58cf8fbc1d1d3e50e2ecbbec43e7abbb5a5b9361c6e559b84f35e93": true,
	"b509c4b6f33ba257c9cafa4bd632bae2233b1dcfb60afdbeb76902fd84c89084": true,
	"b6cb880de1fac7e31084b6bc799de552aaa0319547b7e8b28552844d96dee7fb": true,
	"bb773038271dad48952887b16b7b93c5785d4c84706583c83b3f3d9cd47a6f22": true,
	"bc5078cb0e48d9d04612fa56a5c0ca263b2f398b3b018aff7b7ae4e1097c9295": true,
	"c5189e06ca01559c9ef37216aa5a35a2742627116209b3ad158e94ef6ba8e27f": true,
	"c738f367c69ebf2615d0f2afb17d9b2a18df0b7247b734636e462a64bbcf15f2": true,
	"cbd6491240cc8894d25e366ba83da19df1187f975dc3a5c2f88ce888ca696174": true,
	"d268688399f9c44a4f6049ca71a23383fba05b3ecd62fcf32c1f2dc1de0f3398": true,
	"d2d402b6fa1dae752f8c688d72066a912d7042cc1727213f7990cdb57f60df0c": true,
	"d63618b90d46312bdfa4a060887175b2fccf6c902b40b70e9695c81589eec6d4": true,
	"da42ab0d8238dd86f7669b0ce43bb0a2d0f8bb55147eadc3073760429b703899": true,
	"db9a7b416f368cba18695ebdbe10d702982e8084ea5d213bae72486cb15d5908": true,
	"f227e0c45c9623c90816200f823ef39601190b6c198741a068737eeff5762a4f": true,
	"f46e57f90d9d5b2acd034d4bef431820dcca3a81f43eb5ca18e6afd7eb0b303e": true,
	"f5b3d6ee73694bc909930c3355d5ef4c8ba08902f4c688fce9763f37d3a7e5ab": true,
	"fcbd4ce995c8ec44416299baf56a4b753e30632bc2b53df439b7cee17ed87339": true,
}
