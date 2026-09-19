package main

import (
	"fmt"
	"log/slog"
	"os/user"
	"strconv"

	"goodkind.io/mwan/internal/config"
)

type userIDLookup func(username string) (string, error)

func buildPolicyRuleUIDRange(
	rule config.IfMgrPolicyRuleSection,
	lookup userIDLookup,
) (string, error) {
	logger := slog.Default().With("component", "ifmgr")
	if rule.UIDRange != "" && rule.UIDUser != "" {
		return "", fmt.Errorf("uid_range and uid_user are mutually exclusive")
	}
	if rule.UIDUser == "" {
		return rule.UIDRange, nil
	}
	uid, err := lookup(rule.UIDUser)
	if err != nil {
		logger.Warn("ifmgr: lookup uid_user failed",
			"uid_user", rule.UIDUser, "err", err)
		return "", fmt.Errorf("lookup uid_user %q: %w", rule.UIDUser, err)
	}
	if _, err := strconv.ParseUint(uid, 10, 32); err != nil {
		logger.Warn("ifmgr: uid_user resolved to invalid uid",
			"uid_user", rule.UIDUser, "uid", uid, "err", err)
		return "", fmt.Errorf("uid_user %q resolved to invalid uid %q: %w",
			rule.UIDUser, uid, err)
	}
	return uid + "-" + uid, nil
}

func lookupUserID(username string) (string, error) {
	logger := slog.Default().With("component", "ifmgr")
	account, err := user.Lookup(username)
	if err != nil {
		logger.Warn("ifmgr: user lookup failed",
			"username", username, "err", err)
		return "", fmt.Errorf("user.Lookup(%q): %w", username, err)
	}
	return account.Uid, nil
}
