package probe

// SelfPIDNamespace reads the calling process's own pid namespace (b.146
// rule 14; b.kdf rule 11): the namespace in which the pids it sees name
// processes. A pid recorded by a process in one pid namespace names a
// different process, or none, in another, so a "no such process" read there
// would call a live process gone. A reader of a recorded pid therefore
// compares its own namespace with the one recorded beside the pid, and judges
// the pid only when they are equal.
//
// Contract, exactly one of:
//
//   - (ns, true): ns identifies the namespace; two processes in the same pid
//     namespace read the same ns, and two in different ones read different
//     values. On Linux it is the target of /proc/self/ns/pid verbatim
//     ("pid:[<inode>]"). On darwin, which has no pid namespaces, it is "":
//     every process shares the one namespace.
//   - ("", false): the namespace cannot be read: no mounted /proc, a
//     permission wall, an empty link, or an unsupported OS. Such a reader
//     cannot tell, and never judges a recorded pid gone or alive by it.
//
// It never reads a process environment and never reads the clock.
func SelfPIDNamespace() (ns string, known bool) {
	return selfPIDNamespace()
}
