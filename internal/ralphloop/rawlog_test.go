package ralphloop

// Spec: CS-RLP-014 (raw-log naming) and CS-RLP-032 (the name is UTC whatever
// the container's TZ holds). The tests pass fixed times in non-UTC zones and
// never touch time.Local or TZ.

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("rawLogName (CS-RLP-014, CS-RLP-032)", func() {
	It("CS-RLP-032: formats the clock's time in UTC whatever its zone", func() {
		// 2026-11-01 01:30:15 at UTC-5 is 06:30:15 UTC.
		at := time.Date(2026, 11, 1, 1, 30, 15, 0, time.FixedZone("CDT", -5*3600))
		Expect(rawLogName("/r/runlogs/rawlog", at, 3)).To(Equal("/r/runlogs/rawlog_20261101063015_iter3"))
	})

	It("CS-RLP-032: the loop clock seam is used when set and defaults to time.Now", func() {
		fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 9*3600))
		o := &Options{Now: func() time.Time { return fixed }}
		Expect(o.now()).To(Equal(fixed))
		Expect(rawLogName("b", o.now(), 1)).To(Equal("b_20260101180405_iter1"))
		Expect((&Options{}).now()).To(BeTemporally("~", time.Now(), time.Minute))
	})
})
