package chat

import (
	"context"
	"strings"
)

// The credential reading a chat. Owner is auth.OwnerFromContext, Author the auth.Author name, and
// RunChatID the chat a run key is bound to; a run key owns nothing, so it reaches a personal chat
// only through RunChatID.
type Viewer struct {
	Owner     string
	Author    string
	RunChatID string
}

// Who opens a chat: Name is stored as CreatedBy, Owner as Owner (see Viewer).
type Creator struct {
	Name  string
	Owner string
}

// CreatedBy of the chats the daemon opens itself: the one an agent answers task and thread
// mentions from. No credential signs as it (persons are emails or prefixed ids, keys "key:").
const SystemCreator = "system"

// A chat the daemon opened belongs to the workspace: its agent answers in the task, where everyone
// reads it. A chat a person opened is theirs, even when it is linked to a task.
func (c *Chat) Personal() bool { return c.CreatedBy != SystemCreator }

// Workspace admins are not an exception: a personal chat stays with its creator, and the audit log
// records that it exists.
func (c *Chat) VisibleTo(v Viewer) bool { return audienceOf(c).includes(v) }

type audience struct {
	shared    bool
	owner     string
	createdBy string
	chatID    string
}

func audienceOf(c *Chat) audience {
	if c == nil {
		return audience{}
	}
	return audience{shared: !c.Personal(), owner: c.Owner, createdBy: c.CreatedBy, chatID: c.ID}
}

func (a audience) includes(v Viewer) bool {
	switch {
	case a.shared:
		return true
	case v.RunChatID != "":
		return v.RunChatID == a.chatID
	case a.owner != "":
		return v.Owner != "" && v.Owner == a.owner
	// Rows from before owner was recorded: created_by is the author name, which only an email
	// change can break. A row with neither predates authorship and stays with the workspace.
	case a.createdBy != "":
		return v.Author != "" && v.Author == a.createdBy
	default:
		return true
	}
}

// Listed reports whether v's chat list shows c: a personal chat v may read. The daemon's mention
// chats stay reachable by id but live in their task, not in anyone's list.
func (c *Chat) Listed(v Viewer) bool { return c.Personal() && c.VisibleTo(v) }

// listedClause is Listed in SQL. Run keys are left to the caller: they list at most their own chat.
func listedClause(v Viewer) (string, []any) {
	or := []string{`(owner = '' AND created_by = '')`}
	args := []any{SystemCreator}
	if v.Owner != "" {
		or = append(or, `owner = ?`)
		args = append(args, v.Owner)
	}
	if v.Author != "" {
		or = append(or, `(owner = '' AND created_by = ?)`)
		args = append(args, v.Author)
	}
	return "created_by <> ? AND (" + strings.Join(or, " OR ") + ")", args
}

// ListListed is ListChats for one workspace as v's chat list shows it.
func (s *Store) ListListed(ctx context.Context, workspaceID string, v Viewer, includeArchived bool) ([]Chat, error) {
	clause, args := listedClause(v)
	return s.listChats(ctx, workspaceID, "", "", includeArchived, clause, args...)
}
