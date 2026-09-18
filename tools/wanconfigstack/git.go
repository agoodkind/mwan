package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

var errPinMoved = errors.New("the pinned tag resolves to a different commit than the pinned hash")

// clonePinned checks a component out at its pin: a shallow single-tag clone
// when the pin is a tag, otherwise a full clone checked out at the pinned
// commit (nghttp2-asio pins a commit because it has no release tags). Either
// way the checkout must be the pinned commit hash, so a force-moved upstream
// tag can never change what the release packages.
func (b *builder) clonePinned(ctx context.Context, c component) error {
	dir := b.srcDir(c)
	pin := b.pin(c)
	pinned := plumbing.NewHash(b.commit(c))
	if err := os.RemoveAll(dir); err != nil {
		return b.fail(ctx, "clear source dir", err, slog.String("dir", dir))
	}
	b.log.InfoContext(ctx, "wanconfigstack: clone", "url", c.url, "pin", pin, "commit", pinned.String(), "dir", dir)
	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:           c.url,
		ReferenceName: plumbing.NewTagReferenceName(pin),
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.NoTags,
	})
	if err == nil {
		return b.verifyPinnedHead(ctx, repo, c, pinned)
	}
	if !errors.Is(err, git.NoMatchingRefSpecError{}) && !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return b.fail(ctx, "clone tag", err, slog.String("url", c.url), slog.String("pin", pin))
	}
	if err := os.RemoveAll(dir); err != nil {
		return b.fail(ctx, "clear source dir", err, slog.String("dir", dir))
	}
	repo, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{URL: c.url, NoCheckout: true, Tags: git.NoTags})
	if err != nil {
		return b.fail(ctx, "clone", err, slog.String("url", c.url))
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return b.fail(ctx, "open worktree", err, slog.String("dir", dir))
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: pinned, Force: true}); err != nil {
		return b.fail(ctx, "checkout pinned commit", err, slog.String("dir", dir), slog.String("commit", pinned.String()))
	}
	return nil
}

// verifyPinnedHead refuses a cloned tag whose commit differs from the pinned
// hash.
func (b *builder) verifyPinnedHead(ctx context.Context, repo *git.Repository, c component, pinned plumbing.Hash) error {
	head, err := repo.Head()
	if err != nil {
		return b.fail(ctx, "read cloned head", err, slog.String("component", c.name))
	}
	if head.Hash() != pinned {
		return b.fail(ctx, "verify pinned commit", errPinMoved,
			slog.String("component", c.name), slog.String("pin", b.pin(c)),
			slog.String("resolved", head.Hash().String()), slog.String("pinned", pinned.String()))
	}
	return nil
}
