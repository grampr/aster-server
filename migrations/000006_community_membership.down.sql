DROP TABLE IF EXISTS guild_invites;
DROP TABLE IF EXISTS guild_member_roles;
DROP TABLE IF EXISTS roles;
ALTER TABLE guild_members DROP COLUMN IF EXISTS nickname;
