package clients_test

// Spec: spec/sessions.feature CS-SESS-098 — the presence reader: leases plus
// the host /proc scan. Every /proc is a scratch tree.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
)

const (
	nameA = "claude-sandbox-work-proj-abc123-dunlin"
	nameB = "claude-sandbox-work-other-def456-kestrel"
	// idC shares its first 4 hex with idA, so "aaaa" is ambiguous.
	idC = "aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999"
	me  = 1000
)

var all = []clients.Target{{ID: idA, Name: nameA}, {ID: idB, Name: nameB}, {ID: idC, Name: "claude-sandbox-x-c"}}

// fakeProc writes one process into a scratch /proc.
func fakeProc(root string, pid, uid int, argv ...string) {
	dir := filepath.Join(root, strconv.Itoa(pid))
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)).To(Succeed())
	status := "Name:\tdocker\nUid:\t" + strconv.Itoa(uid) + "\t" + strconv.Itoa(uid) + "\t" + strconv.Itoa(uid) + "\t" + strconv.Itoa(uid) + "\n"
	Expect(os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644)).To(Succeed())
	stat := strconv.Itoa(pid) + " (docker) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345 0 0"
	Expect(os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)).To(Succeed())
}

func inv(argv ...string) (clients.Invocation, bool) { return clients.ParseInvocation(argv) }

// clientOf parses argv and reports whether it is a client of container A.
func clientOf(c clients.Target, argv ...string) bool {
	i, ok := clients.ParseInvocation(argv)
	if !ok {
		return false
	}
	return clients.Proc{Argv: argv, Inv: i}.Of(c, all)
}

var a = clients.Target{ID: idA, Name: nameA}

var _ = Describe("docker CLI argv (CS-SESS-098)", func() {
	It("CS-SESS-098: the launcher's own three client shapes are clients, of the right kind", func() {
		i, ok := inv("docker", "start", "-ai", "--detach-keys=ctrl-q,ctrl-q", nameA)
		Expect(ok).To(BeTrue())
		Expect(i.Kind).To(Equal(clients.KindPrimary))
		Expect(i.Containers).To(Equal([]string{nameA}))

		i, ok = inv("docker", "attach", "--detach-keys=ctrl-q,ctrl-q", idA)
		Expect(ok).To(BeTrue())
		Expect(i.Kind).To(Equal(clients.KindAttach))
		Expect(i.Containers).To(Equal([]string{idA}))

		i, ok = inv("docker", "exec", "-it", "--detach-keys=ctrl-q,ctrl-q", "-u", "rt", "-e", "CLAUDE_SANDBOX_JOINED=1",
			"-e", "TERMINAL_EMULATOR=JetBrains-JediTerm", "-w", "/home/rt/proj", nameA,
			"/usr/bin/env", "-u", "X", "/opt/claude-sandbox/bin/claude-sandbox", "pidslot", "--", "claude", nameB)
		Expect(ok).To(BeTrue())
		Expect(i.Kind).To(Equal(clients.KindJoin))
		Expect(i.Containers).To(Equal([]string{nameA}), "exec's command is not a container")
		Expect(i.Conservative).To(BeFalse())
	})

	It("CS-SESS-098: global options are skipped, with = or separate values; docker container <verb> forms count", func() {
		Expect(clientOf(a, "docker", "--context", "x", "attach", nameA)).To(BeTrue())
		Expect(clientOf(a, "docker", "-c", "x", "-D", "--log-level=debug", "-H", "unix:///s", "attach", nameA)).To(BeTrue())
		Expect(clientOf(a, "/usr/bin/docker", "--config", "/c", "container", "attach", nameA)).To(BeTrue())
		Expect(clientOf(a, "com.docker.cli", "container", "start", "-a", nameA)).To(BeTrue())
		Expect(clientOf(a, "docker", "container", "exec", "-it", nameA, "bash")).To(BeTrue())
	})

	It("CS-SESS-098: start counts only with -a/--attach or -i/--interactive; exec with -d does not count", func() {
		Expect(clientOf(a, "docker", "start", nameA)).To(BeFalse())
		Expect(clientOf(a, "docker", "start", "-i", nameA)).To(BeTrue(), "docker attaches on Attach || OpenStdin")
		Expect(clientOf(a, "docker", "start", "--attach=false", "-i", nameA)).To(BeTrue())
		Expect(clientOf(a, "docker", "start", "--interactive=false", nameA)).To(BeFalse())
		Expect(clientOf(a, "docker", "start", "--attach=false", nameA)).To(BeFalse())
		Expect(clientOf(a, "docker", "start", "-ia", nameA)).To(BeTrue())
		Expect(clientOf(a, "docker", "start", "--attach", nameA)).To(BeTrue())
		Expect(clientOf(a, "docker", "start", nameB, nameA, "-a")).To(BeTrue(), "options are interspersed, every positional is a container")
		Expect(clientOf(a, "docker", "exec", "-d", nameA, "sleep", "9")).To(BeFalse())
		Expect(clientOf(a, "docker", "exec", "-dit", nameA, "sleep", "9")).To(BeFalse())
	})

	It("CS-SESS-098: the container is the positional, never an option's value", func() {
		Expect(clientOf(a, "docker", "exec", "-e", nameA, nameB, "bash")).To(BeFalse())
		Expect(clientOf(a, "docker", "exec", "-u"+nameA, nameB, "bash")).To(BeFalse())
		Expect(clientOf(a, "docker", "exec", "-w", "/x", "--env-file", nameA, nameB)).To(BeFalse())
		Expect(clientOf(a, "docker", "attach", "--detach-keys", nameA, nameB)).To(BeFalse())
		Expect(clientOf(a, "docker", "attach", "--sig-proxy", nameA)).To(BeTrue(), "--sig-proxy is a bool")
		Expect(clientOf(a, "docker", "attach", "--sig-proxy=false", "--no-stdin", nameA)).To(BeTrue())
		Expect(clientOf(a, "docker", "start", "-a", "--checkpoint", nameA, "--checkpoint-dir", "/d", nameB)).To(BeFalse())
		Expect(clientOf(a, "docker", "exec", "-it", "--", nameA, "bash")).To(BeTrue())
	})

	It("CS-SESS-098: an unknown option before the container makes the process count for any container it names", func() {
		i, ok := inv("docker", "exec", "--newflag", "x", nameA, "bash")
		Expect(ok).To(BeTrue())
		Expect(i.Conservative).To(BeTrue())
		Expect(clientOf(a, "docker", "exec", "--newflag", "x", nameA, "bash")).To(BeTrue())
		Expect(clientOf(a, "docker", "--newglobal", "v", "attach", "q", nameA)).To(BeTrue())
		Expect(clientOf(a, "docker", "start", "--newflag", nameA)).To(BeTrue(), "even without -a")
		Expect(clientOf(a, "docker", "exec", "--newflag", nameB, "bash")).To(BeFalse())
	})

	It("CS-SESS-098: docker events, inspect, ps and top never match; nor does a non-docker argv[0]", func() {
		Expect(clientOf(a, "docker", "events", "--since", "1", "--filter", "container="+nameA)).To(BeFalse())
		Expect(clientOf(a, "docker", "inspect", nameA)).To(BeFalse())
		Expect(clientOf(a, "docker", "ps", "-a", "--filter", "name="+nameA)).To(BeFalse())
		Expect(clientOf(a, "docker", "top", nameA, "-o", "pid")).To(BeFalse())
		Expect(clientOf(a, "podman", "attach", nameA)).To(BeFalse())
		Expect(clientOf(a, "bash", "-c", "docker attach "+nameA)).To(BeFalse())
		Expect(clientOf(a, "docker", "attach")).To(BeFalse())
	})

	It("CS-SESS-098: the full id, the name (leading / too) and a unique hex prefix of any length name it; an ambiguous prefix does not", func() {
		Expect(clientOf(a, "docker", "attach", idA)).To(BeTrue())
		Expect(clientOf(a, "docker", "attach", "/"+nameA)).To(BeTrue())
		Expect(clientOf(a, "docker", "attach", idA[:12])).To(BeTrue())
		Expect(clientOf(a, "docker", "attach", "aaaa1")).To(BeTrue(), "unique at 5 hex")
		Expect(clientOf(a, "docker", "attach", "aaaa")).To(BeFalse(), "also idC's prefix")
		Expect(clientOf(a, "docker", "attach", idB[:12])).To(BeFalse())
		Expect(clientOf(a, "docker", "attach", nameA+"x")).To(BeFalse())
	})

	It("CS-SESS-098: a word that is another container's name names that one, even when it is also a prefix of this id", func() {
		others := []clients.Target{a, {ID: idB, Name: "aaaa1"}}
		i, _ := clients.ParseInvocation([]string{"docker", "attach", "aaaa1"})
		Expect(clients.Proc{Inv: i}.Of(a, others)).To(BeFalse())
	})
})

var _ = Describe("the /proc scan and Present (CS-SESS-098)", func() {
	var proc, state string
	BeforeEach(func() {
		proc = GinkgoT().TempDir()
		state = GinkgoT().TempDir()
	})

	It("CS-SESS-098: finds this user's docker clients, ignores another user's, skips unreadable and non-numeric entries", func() {
		fakeProc(proc, 100, me, "docker", "attach", nameA)
		fakeProc(proc, 101, me+1, "docker", "attach", nameA)                        // another user
		fakeProc(proc, 102, me, "docker", "events", "--filter", "container="+nameA) // the OOM watcher
		fakeProc(proc, 103, me, "claude", "--resume", nameA)
		Expect(os.MkdirAll(filepath.Join(proc, "104"), 0o755)).To(Succeed()) // vanished: no files
		fakeProc(proc, 105, me, "docker", "exec", "-it", idA[:12], "bash")
		Expect(os.Remove(filepath.Join(proc, "105", "status"))).To(Succeed()) // status unreadable: skipped
		Expect(os.MkdirAll(filepath.Join(proc, "self"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proc, "stat"), []byte("cpu 1 2\nbtime 1792076685\n"), 0o644)).To(Succeed())

		ps, err := clients.Scan(proc, me)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(1))
		Expect(ps[0].PID).To(Equal(100))
		Expect(ps[0].StartTicks).To(Equal(uint64(12345)))
		boot, err := clients.BootTime(proc)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps[0].Started(boot)).To(Equal(time.Unix(1792076685, 0).Add(123450 * time.Millisecond)))
	})

	It("CS-SESS-098: Present joins held leases and scan hits, by kind and source; a dead lease does not count", func() {
		l, err := clients.Take(state, idA, clients.KindAttach, time.UnixMilli(5), "%3")
		Expect(err).NotTo(HaveOccurred())
		defer l.Release()
		dead := filepath.Join(clients.ContainerDir(state, idA), "1.00.lease")
		Expect(os.WriteFile(dead, []byte(`{"v":1,"kind":"primary","since":1}`), 0o600)).To(Succeed())
		fakeProc(proc, 200, me, "docker", "exec", "-it", nameA, "bash")
		fakeProc(proc, 201, me, "docker", "start", "-ai", nameB)

		p := clients.Present(clients.Options{StateRoot: state, ProcRoot: proc, UID: me}, a, all)
		Expect(p.CannotTell()).To(BeFalse())
		Expect(p.Clients).To(HaveLen(2))
		lease, scan := p.Clients[0], p.Clients[1]
		Expect(lease.Source).To(Equal(clients.SourceLease))
		Expect(lease.Kind).To(Equal(clients.KindAttach))
		Expect(lease.Since).To(Equal(time.UnixMilli(5)))
		Expect(lease.TmuxPane).To(Equal("%3"))
		Expect(lease.Lease).To(Equal(l.Path))
		Expect(scan.Source).To(Equal(clients.SourceScan))
		Expect(scan.Kind).To(Equal(clients.KindJoin))
		Expect(scan.Proc.PID).To(Equal(200))
		Expect(p.Terminal()).To(ConsistOf(lease))
		Expect(p.Joins()).To(ConsistOf(scan))

		l.Release()
		p = clients.Present(clients.Options{StateRoot: state, ProcRoot: proc, UID: me}, a, all)
		Expect(p.Terminal()).To(BeEmpty())
	})

	It("CS-SESS-098: an unreadable /proc or clients dir is cannot tell, never no client", func() {
		p := clients.Present(clients.Options{StateRoot: state, ProcRoot: filepath.Join(proc, "missing"), UID: me}, a, all)
		Expect(p.CannotTell()).To(BeTrue())
		Expect(p.Clients).To(BeEmpty())

		Expect(os.MkdirAll(filepath.Join(state, "clients"), 0o700)).To(Succeed())
		Expect(os.WriteFile(clients.ContainerDir(state, idA), nil, 0o600)).To(Succeed()) // not a directory
		p = clients.Present(clients.Options{StateRoot: state, ProcRoot: proc, UID: me}, a, all)
		Expect(p.CannotTell()).To(BeTrue())
	})

	It("CS-SESS-098: an empty ProcRoot panics under go test", func() {
		Expect(func() { clients.Scan("", me) }).To(PanicWith(ContainSubstring("ProcRoot")))
	})
})
