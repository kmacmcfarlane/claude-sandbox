package imagebuild

// StartDetachedUnguarded is the real detached start without StartDetached's
// go-test refusal, so the setsid mechanics can be tested on a harmless
// command (CS-IMG-045).
var StartDetachedUnguarded = startDetached
