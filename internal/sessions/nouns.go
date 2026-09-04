package sessions

import (
	"math/rand"
	"strconv"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/pidslot"
)

// Instance nouns name a session so it can be referred to by hand — at the
// launch prompt, in `sessions` output, and in `--attach=<noun>`. Typing "otter"
// beats typing claude-sandbox-kmacmcfarlane-claude-sandbox-1de77a.
//
// Selection criteria for the list: 4-8 characters, ASCII, no homophones, no
// entry a prefix of another (so shell completion stays useful), and visually
// distinct from its neighbours.
//
// The list is deliberately short. Other tools pair word lists with a numeric or
// hash tail (docker retries on collision, Heroku appends digits, Codespaces
// appends a hash) because they cannot see which names are already taken. We
// can: PickNoun samples from the unused remainder, so a collision is impossible
// and the list only has to be longer than the number of concurrent sessions in
// one project.
var Nouns = []string{
	"otter", "heron", "badger", "falcon", "walrus", "marmot", "ocelot", "gibbon",
	"lemur", "tapir", "quokka", "vervet", "impala", "kudu", "oryx", "gazelle",
	"cougar", "jaguar", "lynx", "ermine", "fisher", "marten", "weasel", "ferret",
	"beaver", "gopher", "vole", "shrew", "pika", "hyrax", "aardvark", "pangolin",
	"tamarin", "macaque", "colobus", "langur", "howler", "saki", "titi", "uakari",
	"toucan", "quetzal", "kestrel", "merlin", "osprey", "harrier", "buzzard", "kite",
	"petrel", "fulmar", "gannet", "puffin", "murre", "auklet", "skua", "noddy",
	"curlew", "godwit", "dunlin", "plover", "avocet", "stilt", "jacana", "rail",
	"crake", "bittern", "egret", "ibis", "stork", "snipe", "flamingo", "grebe",
	"loon", "garganey", "pintail", "wigeon", "teal", "eider", "scoter", "smew",
	"salmon", "trout", "grayling", "tarpon", "bonito", "wahoo", "marlin", "opah",
	"tetra", "danio", "barbel", "chub", "rudd", "tench", "bream", "gudgeon",
	"cicada", "mantis", "firefly", "beetle", "weevil", "cricket", "locust", "hornet",
	"cypress", "juniper", "hemlock", "spruce", "alder", "rowan", "hazel", "willow",
	"basalt", "granite", "gabbro", "gneiss", "schist", "quartz", "jasper", "onyx",
	"comet", "quasar", "pulsar", "nebula", "zenith", "apogee", "syzygy", "umbra",
}

// PickNoun returns a noun not present in inUse (CS-SESS-007). The chooser is
// injected so names are deterministic under test (CS-SESS-009); pass nil for
// real randomness.
func PickNoun(inUse []string, chooser func(n int) int) string {
	taken := make(map[string]bool, len(inUse))
	for _, u := range inUse {
		taken[strings.TrimSpace(u)] = true
	}
	free := make([]string, 0, len(Nouns))
	for _, n := range Nouns {
		if !taken[n] {
			free = append(free, n)
		}
	}
	if chooser == nil {
		chooser = rand.Intn
	}
	if len(free) == 0 {
		// Every noun is in use — implausible in one project, but the name must
		// still be unique, so fall back to a suffixed one (CS-SESS-008).
		return suffixed(Nouns, taken, chooser)
	}
	return free[clamp(chooser(len(free)), len(free))]
}

// PickClass returns a pid class in [0, pidslot.Modulus) not present in inUse
// (CS-PID-004), sampled without replacement exactly like nouns. The chooser is
// injected for determinism under test; nil means real randomness. When every
// class is taken — more concurrent sandboxes than classes — any class is
// returned: a collision is then unavoidable and the launch still proceeds.
func PickClass(inUse []string, chooser func(n int) int) int {
	taken := make(map[int]bool, len(inUse))
	for _, u := range inUse {
		if k, err := strconv.Atoi(strings.TrimSpace(u)); err == nil {
			taken[k] = true
		}
	}
	free := make([]int, 0, pidslot.Modulus)
	for k := 0; k < pidslot.Modulus; k++ {
		if !taken[k] {
			free = append(free, k)
		}
	}
	if chooser == nil {
		chooser = rand.Intn
	}
	if len(free) == 0 {
		return clamp(chooser(pidslot.Modulus), pidslot.Modulus)
	}
	return free[clamp(chooser(len(free)), len(free))]
}

// suffixed finds a "<noun>-N" that is not taken.
func suffixed(pool []string, taken map[string]bool, chooser func(n int) int) string {
	base := pool[clamp(chooser(len(pool)), len(pool))]
	for n := 2; ; n++ {
		cand := base + "-" + itoa(n)
		if !taken[cand] {
			return cand
		}
	}
}

func clamp(i, n int) int {
	if i < 0 || i >= n {
		return 0
	}
	return i
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
