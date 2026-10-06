package chat

import "math"

// Permission bits follow the PermissionBits schema of Aster Protocol.
const (
	PermViewChannel    int64 = 1 << 0
	PermSendMessages   int64 = 1 << 1
	PermManageMessages int64 = 1 << 2
	PermManageChannels int64 = 1 << 3
	PermManageGuild    int64 = 1 << 4
	PermManageRoles    int64 = 1 << 5
	PermManageMembers  int64 = 1 << 6
	PermCreateInvite   int64 = 1 << 7
	PermConnect        int64 = 1 << 8
	PermSpeak          int64 = 1 << 9
	PermStream         int64 = 1 << 10

	AllPermissions int64 = 1<<11 - 1

	// DefaultRolePermissions is granted to every Member through the default Role.
	DefaultRolePermissions = PermViewChannel | PermSendMessages | PermConnect | PermSpeak | PermStream
)

// Access is the effective authority of one Member in one Guild.
type Access struct {
	Owner       bool
	Permissions int64
	// TopPosition is the position of the Member's highest Role. The Owner outranks everyone.
	TopPosition int
}

func ownerAccess() Access {
	return Access{Owner: true, Permissions: AllPermissions, TopPosition: math.MaxInt32}
}

func (a Access) Has(permission int64) bool {
	return a.Owner || a.Permissions&permission == permission
}

// Outranks reports whether a Role at position can be managed by this Member.
func (a Access) Outranks(position int) bool {
	return a.Owner || a.TopPosition > position
}
