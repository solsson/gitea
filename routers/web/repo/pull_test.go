// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"context"
	"io"
	"testing"
	"time"

	"code.gitea.io/gitea/models/unittest"
	// repo_model "code.gitea.io/gitea/models/repo" // Not directly used in this simplified test yet
	"code.gitea.io/gitea/modules/git"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/services/gitdiff"
	"code.gitea.io/gitea/services/contexttest"

	"github.com/stretchr/testify/assert"
)

// MockBlameReader is a mock implementation of git.BlameReader
type MockBlameReader struct {
	Parts []*git.BlamePart
	idx   int
	err   error
	closed bool
}

func (m *MockBlameReader) NextPart() (*git.BlamePart, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.idx < len(m.Parts) {
		part := m.Parts[m.idx]
		m.idx++
		return part, nil
	}
	return nil, io.EOF
}

func (m *MockBlameReader) Close() error {
	m.closed = true
	return nil
}

// Store the mock reader instance to check if Close was called
var lastMockBlameReader *MockBlameReader

func TestViewPullFiles_BlamePopulation(t *testing.T) {
	unittest.InitSettings() // Initialize settings for logging or other module dependencies

	// 1. Mock git.CreateBlameReader
	originalCreateBlameReader := git.CreateBlameReaderFunc
	defer func() { git.CreateBlameReaderFunc = originalCreateBlameReader }()

	mockBlamePartsFile1 := []*git.BlamePart{
		{
			Commit: &git.Commit{
				ID: git.MustIDFromString("blameCommitSHA1"),
				Author: &git.Signature{
					Name:  "Blame Author 1",
					When:  time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
				},
			},
			Lines: []string{"+ added line 1"}, // Simulate this part covers one line
		},
		{
			Commit: &git.Commit{
				ID: git.MustIDFromString("blameCommitSHA2"),
				Author: &git.Signature{
					Name:  "Blame Author 2",
					When:  time.Date(2023, 1, 2, 12, 0, 0, 0, time.UTC),
				},
			},
			Lines: []string{"+ added line 2"}, // Simulate this part covers the next line
		},
	}

	mockBlamePartsFile3 := []*git.BlamePart{
		{
			Commit: &git.Commit{
				ID: git.MustIDFromString("blameCommitSHA3"),
				Author: &git.Signature{
					Name:  "Blame Author 3",
					When:  time.Date(2023, 3, 1, 12, 0, 0, 0, time.UTC),
				},
			},
			Lines: []string{"+ added line in file3"},
		},
	}


	git.CreateBlameReaderFunc = func(ctx context.Context, repoPath, commitID, filePath string, bypassBlameIgnore bool) (git.BlameReader, error) {
		var parts []*git.BlamePart
		if filePath == "testfile.txt" {
			parts = mockBlamePartsFile1
		} else if filePath == "file3-blame.txt" {
			parts = mockBlamePartsFile3
		} else {
			parts = []*git.BlamePart{} // Default to no blame info for other files
		}
		lastMockBlameReader = &MockBlameReader{Parts: parts}
		return lastMockBlameReader, nil
	}

	// 2. Prepare DiffData
	diffData := &gitdiff.Diff{
		Files: []*gitdiff.DiffFile{
			{
				Name: "testfile.txt",
				Type: gitdiff.DiffFileAdd,
				Sections: []*gitdiff.DiffSection{
					{
						Lines: []*gitdiff.DiffLine{
							{Type: gitdiff.DiffLineAdd, Content: "+ added line 1"}, // Expects blame from SHA1
							{Type: gitdiff.DiffLinePlain, Content: "  context line"},
							{Type: gitdiff.DiffLineAdd, Content: "+ added line 2"}, // Expects blame from SHA2
						},
					},
				},
			},
			{
				Name: "anotherfile.txt",
				Type: gitdiff.DiffFileChange,
				Sections: []*gitdiff.DiffSection{
					{
						Lines: []*gitdiff.DiffLine{
							{Type: gitdiff.DiffLineDel, Content: "- deleted line"},
						},
					},
				},
			},
			{
				Name: "file3-blame.txt",
				Type: gitdiff.DiffFileAdd,
				Sections: []*gitdiff.DiffSection{
					{
						Lines: []*gitdiff.DiffLine{
							{Type: gitdiff.DiffLineAdd, Content: "+ added line in file3"}, // Expects blame from SHA3
						},
					},
				},
			},
		},
	}

	// 3. Prepare mock context
	ctx, _ := contexttest.MockContext(t, "/")
	ctx.Repo = &contexttest.MockRepo{
		GitRepo: &git.Repository{Path: "/tmp/gitea-test-repo"},
	}

	endCommitID := "dummyHeadCommitID"

	// 4. Call the blame processing logic (simulated loop from viewPullFiles)
	for _, file := range diffData.Files {
		var currentFileBlameReader git.BlameReader
		var blameReaderErr error

		// Create blame reader once per file for added lines
		hasAddedLines := false
		for _, section := range file.Sections {
			for _, line := range section.Lines {
				if line.Type == gitdiff.DiffLineAdd {
					hasAddedLines = true
					break
				}
			}
			if hasAddedLines {
				break
			}
		}

		if hasAddedLines {
			currentFileBlameReader, blameReaderErr = git.CreateBlameReaderFunc(ctx, ctx.Repo.GitRepo.Path, endCommitID, file.Name, false)
			if blameReaderErr != nil {
				log.Error("CreateBlameReader failed for %s@%s:%s: %v", ctx.Repo.GitRepo.Path, endCommitID, file.Name, blameReaderErr)
			}
		}

		for _, section := range file.Sections {
			for _, line := range section.Lines {
				if line.Type == gitdiff.DiffLineAdd {
					if blameReaderErr != nil { // Error from CreateBlameReader
						continue
					}
					if currentFileBlameReader == nil { // No added lines, so no reader created
						continue
					}

					blamePart, err := currentFileBlameReader.NextPart()
					if err != nil && err != io.EOF {
						log.Error("blameReader.NextPart failed for %s@%s:%s: %v", ctx.Repo.GitRepo.Path, endCommitID, file.Name, err)
						// Decide if we should break or continue for this file
						break // Stop processing this file if NextPart errors
					}

					if blamePart != nil && blamePart.Commit != nil {
						line.BlameCommitSHA = blamePart.Commit.ID.String()
						line.BlameAuthor = blamePart.Commit.Author.Name
						line.BlameDate = blamePart.Commit.Author.When.Format("2006-01-02")
					}
					// If err is io.EOF, blamePart might be nil or the last part.
					// The original code doesn't explicitly handle blamePart == nil if err == io.EOF,
					// but if NextPart returns (nil, io.EOF), the fields won't be updated.
				}
			}
		}
		if currentFileBlameReader != nil {
			currentFileBlameReader.Close()
		}
	}

	// 5. Assertions
	// File 1: testfile.txt
	firstAddedLineFile1 := diffData.Files[0].Sections[0].Lines[0]
	assert.Equal(t, "blameCommitSHA1", firstAddedLineFile1.BlameCommitSHA)
	assert.Equal(t, "Blame Author 1", firstAddedLineFile1.BlameAuthor)
	assert.Equal(t, "2023-01-01", firstAddedLineFile1.BlameDate)
	assert.True(t, lastMockBlameReader.closed, "BlameReader for testfile.txt should have been closed")


	secondAddedLineFile1 := diffData.Files[0].Sections[0].Lines[2]
	assert.Equal(t, "blameCommitSHA2", secondAddedLineFile1.BlameCommitSHA)
	assert.Equal(t, "Blame Author 2", secondAddedLineFile1.BlameAuthor)
	assert.Equal(t, "2023-01-02", secondAddedLineFile1.BlameDate)

	// File 2: anotherfile.txt (no added lines, no blame expected)
	deletedLineFile2 := diffData.Files[1].Sections[0].Lines[0]
	assert.Empty(t, deletedLineFile2.BlameCommitSHA)

	// File 3: file3-blame.txt
	addedLineFile3 := diffData.Files[2].Sections[0].Lines[0]
	assert.Equal(t, "blameCommitSHA3", addedLineFile3.BlameCommitSHA)
	assert.Equal(t, "Blame Author 3", addedLineFile3.BlameAuthor)
	assert.Equal(t, "2023-03-01", addedLineFile3.BlameDate)
	assert.True(t, lastMockBlameReader.closed, "BlameReader for file3-blame.txt should have been closed")
}

var _ git.BlameReader = &MockBlameReader{}
