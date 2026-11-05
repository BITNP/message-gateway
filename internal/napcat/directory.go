package napcat

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"
)

type friendEntry struct {
	UserID int64 `json:"user_id"`
}

type groupEntry struct {
	GroupID int64 `json:"group_id"`
}

type memberEntry struct {
	UserID int64 `json:"user_id"`
}

// memberDirectory is the reverse index of which shared groups each user
// belongs to. It is populated by SyncDirectory and refreshed periodically.
type memberDirectory struct {
	mu           sync.RWMutex
	friends      map[int64]struct{}
	memberGroups map[int64][]int64
	loaded       bool
}

func newMemberDirectory() *memberDirectory {
	return &memberDirectory{
		friends:      make(map[int64]struct{}),
		memberGroups: make(map[int64][]int64),
	}
}

func (d *memberDirectory) Replace(friends []int64, memberGroups map[int64][]int64) {
	friendSet := make(map[int64]struct{}, len(friends))
	for _, userID := range friends {
		if userID > 0 {
			friendSet[userID] = struct{}{}
		}
	}

	groupsByUser := make(map[int64][]int64, len(memberGroups))
	for userID, groups := range memberGroups {
		if userID <= 0 {
			continue
		}
		copied := append([]int64(nil), groups...)
		slices.Sort(copied)
		groupsByUser[userID] = copied
	}

	d.mu.Lock()
	d.friends = friendSet
	d.memberGroups = groupsByUser
	d.loaded = true
	d.mu.Unlock()
}

func (d *memberDirectory) IsFriend(userID int64) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.friends[userID]
	return ok
}

func (d *memberDirectory) Groups(userID int64) []int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()

	groups := d.memberGroups[userID]
	if len(groups) == 0 {
		return nil
	}
	out := make([]int64, len(groups))
	copy(out, groups)
	return out
}

func (d *memberDirectory) Loaded() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.loaded
}

// SyncDirectory loads the bot's friend list, group list, and each group's
// members. A failure for one group is logged and skipped so the rest of the
// directory remains usable.
func (c *Client) SyncDirectory(ctx context.Context) error {
	var friends []friendEntry
	if err := c.call(ctx, "get_friend_list", nil, &friends); err != nil {
		return fmt.Errorf("napcat: get friend list: %w", err)
	}

	var groups []groupEntry
	if err := c.call(ctx, "get_group_list", nil, &groups); err != nil {
		return fmt.Errorf("napcat: get group list: %w", err)
	}

	groupMembers := make(map[int64][]int64, len(groups))
	for _, group := range groups {
		if group.GroupID <= 0 {
			continue
		}

		var members []memberEntry
		params := map[string]int64{"group_id": group.GroupID}
		if err := c.call(ctx, "get_group_member_list", params, &members); err != nil {
			log.Printf("napcat: get group member list for %d failed: %v", group.GroupID, err)
			continue
		}

		memberIDs := make([]int64, 0, len(members))
		for _, member := range members {
			if member.UserID > 0 {
				memberIDs = append(memberIDs, member.UserID)
			}
		}
		groupMembers[group.GroupID] = memberIDs
	}

	friendIDs := make([]int64, 0, len(friends))
	for _, friend := range friends {
		if friend.UserID > 0 {
			friendIDs = append(friendIDs, friend.UserID)
		}
	}

	c.directory.Replace(friendIDs, buildMemberGroups(groupMembers))
	log.Printf(
		"napcat: directory synced friends=%d groups=%d member_groups=%d",
		len(friendIDs),
		len(groups),
		len(groupMembers),
	)
	return nil
}

// Run keeps the member directory fresh: it retries the initial load until it
// succeeds, then refreshes on the configured interval.
func (c *Client) Run(ctx context.Context) {
	for {
		err := c.syncWithTimeout(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("napcat: directory sync failed, retrying: %v", err)
		if !waitFor(ctx, c.cfg.DirectoryRetry) {
			return
		}
	}

	ticker := time.NewTicker(c.cfg.DirectoryRefresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.syncWithTimeout(ctx); err != nil && ctx.Err() == nil {
				log.Printf("napcat: directory refresh failed: %v", err)
			}
		}
	}
}

func (c *Client) syncWithTimeout(ctx context.Context) error {
	syncCtx, cancel := context.WithTimeout(ctx, c.cfg.DirectoryTimeout)
	defer cancel()
	return c.SyncDirectory(syncCtx)
}

func buildMemberGroups(groupMembers map[int64][]int64) map[int64][]int64 {
	sets := make(map[int64]map[int64]struct{})
	for groupID, members := range groupMembers {
		for _, userID := range members {
			if userID <= 0 {
				continue
			}
			set := sets[userID]
			if set == nil {
				set = make(map[int64]struct{})
				sets[userID] = set
			}
			set[groupID] = struct{}{}
		}
	}

	groupsByUser := make(map[int64][]int64, len(sets))
	for userID, set := range sets {
		groups := make([]int64, 0, len(set))
		for groupID := range set {
			groups = append(groups, groupID)
		}
		slices.Sort(groups)
		groupsByUser[userID] = groups
	}
	return groupsByUser
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
