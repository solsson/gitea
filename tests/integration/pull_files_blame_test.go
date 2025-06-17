// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	auth_model "code.gitea.io/gitea/models/auth"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unittest"
	user_model "code.gitea.io/gitea/models/user"
	"code.gitea.io/gitea/modules/git"
	"code.gitea.io/gitea/modules/setting"
	"code.gitea.io/gitea/modules/test"
	"code.gitea.io/gitea/tests"

	"github.com/stretchr/testify/assert"
)

func TestPullFilesViewBlame(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// User A (owner of the repo)
	userA := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})
	// User B (will make a commit)
	userB := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user4"})

	// Create a repository owned by User A
	repo, err := repo_model.CreateRepository(unittest.DefaultDBContext(), userA, userA, repo_model.CreateRepoOptions{
		Name:        "repo-blame-test",
		Description: "Temporary repository for blame test",
		AutoInit:    true,
		IsPrivate:   false,
		OwnerID:     userA.ID,
	})
	assert.NoError(t, err)
	defer func() {
		if repo != nil {
			unittest.DeleteRepositories(t, repo.ID)
		}
	}()

	repoPath := repo.RepoPath()

	// --- Commit 1 (by User A, on default branch initially) ---
	commitTime1 := time.Date(2023, 1, 1, 10, 0, 0, 0, time.FixedZone("UTC+0", 0))
	filePath := "file.txt"
	contentLine1 := "This is line 1 by user A\n"

	err = git.CreateOrUpdateFileInPath(git.CreateOrUpdateFileOptions{
		Path:          repoPath,
		Branch:        repo.DefaultBranch,
		Content:       contentLine1,
		RelativePath:  filePath,
		Author:        userA.NewGitSig(),
		Committer:     userA.NewGitSig(),
		Message:       "Add line1 by userA",
		CommitCreated: &commitTime1,
	})
	assert.NoError(t, err)
	commitID1, err := git.GetFullCommitID(git.DefaultContext, repoPath, repo.DefaultBranch)
	assert.NoError(t, err)

	// --- Create new branch "blame_branch" from default branch ---
	blameBranchName := "blame_branch"
	err = git.CreateBranch(git.DefaultContext, repoPath, repo.DefaultBranch, blameBranchName)
	assert.NoError(t, err)

	// --- Commit 2 (by User B, on blame_branch) ---
	commitTime2 := time.Date(2023, 1, 2, 11, 0, 0, 0, time.FixedZone("UTC+0", 0))
	contentLine2 := "This is line 2 by user B\n"
	currentContentCommit2 := contentLine1 + contentLine2

	err = git.CreateOrUpdateFileInPath(git.CreateOrUpdateFileOptions{
		Path:          repoPath,
		Branch:        blameBranchName,
		Content:       currentContentCommit2,
		RelativePath:  filePath,
		Author:        userB.NewGitSig(),
		Committer:     userB.NewGitSig(),
		Message:       "Add line2 by userB",
		OldCommitID:   commitID1, // ensure it's based on previous commit
		CommitCreated: &commitTime2,
	})
	assert.NoError(t, err)
	commitID2, err := git.GetFullCommitID(git.DefaultContext, repoPath, blameBranchName)
	assert.NoError(t, err)

	// --- Commit 3 (by User A, on blame_branch) ---
	commitTime3 := time.Date(2023, 1, 3, 12, 0, 0, 0, time.FixedZone("UTC+0", 0))
	contentLine3 := "This is line 3 by user A again\n"
	currentContentCommit3 := currentContentCommit2 + contentLine3

	err = git.CreateOrUpdateFileInPath(git.CreateOrUpdateFileOptions{
		Path:          repoPath,
		Branch:        blameBranchName,
		Content:       currentContentCommit3,
		RelativePath:  filePath,
		Author:        userA.NewGitSig(),
		Committer:     userA.NewGitSig(),
		Message:       "Add line3 by userA",
		OldCommitID:   commitID2,
		CommitCreated: &commitTime3,
	})
	assert.NoError(t, err)
	// commitID3, err := git.GetFullCommitID(git.DefaultContext, repoPath, blameBranchName)
	// assert.NoError(t, err)


	// Create Pull Request
	// User A (owner) creates the PR from userA:blame_branch to userA:main (default)
	session := loginUser(t, userA.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	pr, err := tests.CreatePullRequest(t, userA, userA, token, repo.FullName(), blameBranchName, repo.DefaultBranch, "PR for Blame Test")
	assert.NoError(t, err)

	// Navigate to PR files changed page
	prFilesLink := fmt.Sprintf("/%s/%s/pulls/%d/files", userA.Name, repo.Name, pr.Index)
	req := NewRequest(t, "GET", prFilesLink)
	resp := session.MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// --- Assertions for blame information ---
	// We expect blame info for lines added in this PR.
	// Line 1 ("This is line 1 by user A") was on the base branch, so it's context, not an added line in this PR's diff.
	// Line 2 ("This is line 2 by user B") was added by commitID2 by userB.
	// Line 3 ("This is line 3 by user A again") was added by commitID3 by userA.

	// Selector for an added line in the diff. This needs to be specific enough.
	// Example: find a row with class "add-code" containing "This is line 2 by user B"
	// Then, within that row, find the ".diff-blame-info" span.

	// Assertion for Line 2 (added by User B)
	// The line content in the diff will be prefixed with "+"
	line2Selector := fmt.Sprintf(`tr.add-code td.lines-code:contains("%s")`, strings.TrimSpace(contentLine2))
	blameInfoSelectorLine2 := line2Selector + ` span.diff-blame-info`

	htmlDoc.AssertElement(t, blameInfoSelectorLine2, true) // Check if blame info exists
	blameTextLine2 := strings.TrimSpace(htmlDoc.Find(blameInfoSelectorLine2).Text())

	assert.Contains(t, blameTextLine2, userB.Name, "Blame author for line 2 should be userB")
	assert.Contains(t, blameTextLine2, git.ShortSha(commitID2), "Blame commit for line 2 should be commitID2")
	assert.Contains(t, blameTextLine2, commitTime2.Format("2006-01-02"), "Blame date for line 2 should be correct")

	blameLinkLine2 := htmlDoc.Find(blameInfoSelectorLine2 + " a").Attr("href")
	expectedLinkLine2 := fmt.Sprintf("/%s/%s/commit/%s", userA.Name, repo.Name, commitID2)
	assert.Equal(t, expectedLinkLine2, blameLinkLine2, "Blame commit link for line 2 is incorrect")

	// Assertion for Line 3 (added by User A, in the same PR)
	// We need commitID3 for this, which wasn't stored earlier, let's get it now.
	// Note: In a real test, ensure this commit is distinct enough if userA also made commitID1.
	// Here, the commit message and time will differ.
	commitID3AfterPR, err := git.GetFullCommitID(git.DefaultContext, repoPath, blameBranchName)
	assert.NoError(t, err)

	line3Selector := fmt.Sprintf(`tr.add-code td.lines-code:contains("%s")`, strings.TrimSpace(contentLine3))
	blameInfoSelectorLine3 := line3Selector + ` span.diff-blame-info`

	htmlDoc.AssertElement(t, blameInfoSelectorLine3, true)
	blameTextLine3 := strings.TrimSpace(htmlDoc.Find(blameInfoSelectorLine3).Text())

	assert.Contains(t, blameTextLine3, userA.Name, "Blame author for line 3 should be userA")
	assert.Contains(t, blameTextLine3, git.ShortSha(commitID3AfterPR), "Blame commit for line 3 should be commitID3")
	assert.Contains(t, blameTextLine3, commitTime3.Format("2006-01-02"), "Blame date for line 3 should be correct")

	blameLinkLine3 := htmlDoc.Find(blameInfoSelectorLine3 + " a").Attr("href")
	expectedLinkLine3 := fmt.Sprintf("/%s/%s/commit/%s", userA.Name, repo.Name, commitID3AfterPR)
	assert.Equal(t, expectedLinkLine3, blameLinkLine3, "Blame commit link for line 3 is incorrect")
}
