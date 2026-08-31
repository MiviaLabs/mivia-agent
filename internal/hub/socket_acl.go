package hub

// hubPipeSDDL is the security descriptor for the Windows named pipe, in SDDL
// form. It is the Windows equivalent of the Unix listener's chmod 0600.
//
//	D:P        a DACL that is PROTECTED, so it inherits no ACEs from anywhere
//	(A;;GA;;;OW)  allow GENERIC_ALL to the OWNER of the object - this user
//	(A;;GA;;;SY)  allow GENERIC_ALL to LOCAL SYSTEM
//
// Nothing else is granted, so no other local account can open the pipe.
//
// This matters because the hub authenticates nobody: owner.accept registers
// whatever connects as a client and broadcast immediately fans out every
// session sharing the workspace - including KindTurnStart's Detail, which is
// the user's own submitted prompt. The pipe name is a SHA-256 of the store
// path, which is guessable, not a secret. Passing nil here (as this did) makes
// go-winio fall back to the system default named-pipe DACL, which grants read
// access to every local user.
//
// It deliberately lives in an untagged file rather than inside
// socket_windows.go: a Windows-only constant cannot be checked by anyone
// developing or running CI on Linux, and the absence of any such check is
// exactly how the two platforms came to disagree. See
// TestHubPipeSDDLGrantsOnlyOwnerAndSystem.
const hubPipeSDDL = "D:P(A;;GA;;;OW)(A;;GA;;;SY)"
