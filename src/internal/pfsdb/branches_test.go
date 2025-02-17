package pfsdb_test

import (
	"context"
	"fmt"
	"github.com/pachyderm/pachyderm/v2/src/internal/uuid"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pachyderm/pachyderm/v2/src/internal/errors"
	"github.com/pachyderm/pachyderm/v2/src/internal/pachsql"
	"github.com/pachyderm/pachyderm/v2/src/internal/pctx"
	"github.com/pachyderm/pachyderm/v2/src/internal/pfsdb"
	"github.com/pachyderm/pachyderm/v2/src/internal/require"
	"github.com/pachyderm/pachyderm/v2/src/internal/stream"
	"github.com/pachyderm/pachyderm/v2/src/internal/testutil/random"
	"github.com/pachyderm/pachyderm/v2/src/pfs"
)

func compareBranchOpts() []cmp.Option {
	return []cmp.Option{
		protocmp.FilterField(new(pfs.BranchInfo), "created_at", cmp.Ignore()),
		protocmp.FilterField(new(pfs.BranchInfo), "updated_at", cmp.Ignore()),
		cmpopts.SortSlices(func(a, b *pfs.Branch) bool { return a.Key() < b.Key() }), // Note that this is before compareBranch because we need to sort first.
		cmpopts.SortMaps(func(a, b pfsdb.BranchID) bool { return a < b }),
		cmpopts.EquateEmpty(),
		protocmp.Transform(),
		protocmp.IgnoreEmptyMessages(),
	}
}

func newProjectInfo(name string) *pfs.ProjectInfo {
	return &pfs.ProjectInfo{
		Project: &pfs.Project{
			Name: name,
		},
		Description: "test project",
	}
}

func newRepoInfo(project *pfs.Project, name, repoType string) *pfs.RepoInfo {
	return &pfs.RepoInfo{
		Repo: &pfs.Repo{
			Project: project,
			Name:    name,
			Type:    repoType,
		},
		Description: "test repo",
	}
}

func newCommitInfo(repo *pfs.Repo, id string, parent *pfs.Commit) *pfs.CommitInfo {
	return &pfs.CommitInfo{
		Commit: &pfs.Commit{
			Repo:   repo,
			Id:     id,
			Branch: &pfs.Branch{},
		},
		Description:  "test commit",
		ParentCommit: parent,
		Origin:       &pfs.CommitOrigin{Kind: pfs.OriginKind_AUTO},
		Metadata:     map[string]string{"key": "value"},
		CreatedBy:    "the_tests",
		Started:      timestamppb.New(time.Now()),
	}
}

func createProject(t *testing.T, ctx context.Context, tx *pachsql.Tx, projectInfo *pfs.ProjectInfo) {
	t.Helper()
	require.NoError(t, pfsdb.UpsertProject(ctx, tx, projectInfo))
}

func createRepo(t *testing.T, ctx context.Context, tx *pachsql.Tx, repoInfo *pfs.RepoInfo) *pfsdb.Repo {
	t.Helper()
	createProject(t, ctx, tx, newProjectInfo(repoInfo.Repo.Project.Name))
	id, err := pfsdb.UpsertRepo(ctx, tx, repoInfo)
	require.NoError(t, err)
	return &pfsdb.Repo{ID: id, RepoInfo: repoInfo}
}

func createCommit(t *testing.T, ctx context.Context, tx *pachsql.Tx, commitInfo *pfs.CommitInfo) *pfsdb.Commit {
	t.Helper()
	createRepo(t, ctx, tx, newRepoInfo(commitInfo.Commit.Repo.Project, commitInfo.Commit.Repo.Name, commitInfo.Commit.Repo.Type))
	commitID, err := pfsdb.CreateCommit(ctx, tx, commitInfo)
	require.NoError(t, err)
	return &pfsdb.Commit{ID: commitID, CommitInfo: commitInfo}
}

func TestGetBranchByNameMissingRepo(t *testing.T) {
	t.Parallel()
	withDB(t, func(ctx context.Context, t *testing.T, db *pachsql.DB) {
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			repoInfo := newRepoInfo(&pfs.Project{Name: "default"}, "repo1", pfs.UserRepoType)
			branchInfo := &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoInfo.Repo,
					Name: "master",
				},
			}
			_, err := pfsdb.GetBranch(ctx, tx, branchInfo.Branch)
			require.True(t, errors.As(err, &pfsdb.RepoNotFoundError{}))
		})
	})

}

func TestBranchUpsert(t *testing.T) {
	t.Parallel()
	withDB(t, func(ctx context.Context, t *testing.T, db *pachsql.DB) {
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			repoInfo := newRepoInfo(&pfs.Project{Name: "project1"}, "repo1", pfs.UserRepoType)
			commit1 := createCommit(t, ctx, tx, newCommitInfo(repoInfo.Repo, random.String(32), nil))
			branchInfo := &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoInfo.Repo,
					Name: "master",
				},
				Head:      commit1.CommitInfo.Commit,
				CreatedBy: "the_tests",
			}
			id, err := pfsdb.UpsertBranch(ctx, tx, branchInfo)
			require.NoError(t, err)
			gotBranchInfo, err := pfsdb.GetBranchInfo(ctx, tx, id)
			require.NoError(t, err)
			require.NoDiff(t, branchInfo, gotBranchInfo, compareBranchOpts())
			gotBranchByName, err := pfsdb.GetBranch(ctx, tx, branchInfo.Branch)
			require.NoError(t, err)
			require.NoDiff(t, branchInfo, gotBranchByName.BranchInfo, compareBranchOpts())

			// Update branch to point to second commit
			commit2 := createCommit(t, ctx, tx, newCommitInfo(repoInfo.Repo, random.String(32), commit1.CommitInfo.Commit))
			branchInfo.Head = commit2.CommitInfo.Commit
			id2, err := pfsdb.UpsertBranch(ctx, tx, branchInfo)
			require.NoError(t, err)
			require.Equal(t, id, id2, "UpsertBranch should keep id stable")
			gotBranchInfo2, err := pfsdb.GetBranchInfo(ctx, tx, id2)
			require.NoError(t, err)
			require.NoDiff(t, branchInfo, gotBranchInfo2, compareBranchOpts())

			// Change metadata
			branchInfo.Metadata = map[string]string{"new key": "new value"}
			id3, err := pfsdb.UpsertBranch(ctx, tx, branchInfo)
			require.NoError(t, err)
			require.Equal(t, id, id3, "UpsertBranch should keep id stable")
			gotBranchInfo3, err := pfsdb.GetBranchInfo(ctx, tx, id3)
			require.NoError(t, err)
			require.NoDiff(t, branchInfo, gotBranchInfo3, compareBranchOpts())

			// Attempt to change creator.
			branchInfo.CreatedBy = ""
			id4, err := pfsdb.UpsertBranch(ctx, tx, branchInfo)
			require.NoError(t, err)
			require.Equal(t, id, id4, "UpsertBranch should keep id stable")
			gotBranchInfo4, err := pfsdb.GetBranchInfo(ctx, tx, id4)
			require.NoError(t, err)
			require.NoDiff(t, gotBranchInfo3, gotBranchInfo4, compareBranchOpts())
		})
	})
}

func TestBranchInfoWithProvenance(t *testing.T) {
	t.Parallel()
	size := 10
	branches := make(map[int]*pfsdb.Branch)
	withDB(t, func(ctx context.Context, t *testing.T, db *pachsql.DB) {
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			for i := 1; i <= size; i++ { // row ID in postgres starts at 1.
				commit := testCommit(ctx, t, tx, fmt.Sprintf("r%d", i))
				_, err := pfsdb.CreateCommit(ctx, tx, commit)
				require.NoError(t, err, "should be able to create commit")
				branch := &pfs.BranchInfo{
					Branch: &pfs.Branch{
						Name: "master",
						Repo: commit.Commit.Repo,
					},
					Head: commit.Commit,
				}
				if i > 1 { // make every branch provenant on the branch before it.
					branch.DirectProvenance = []*pfs.Branch{branches[i-1].Branch}
				}
				id, err := pfsdb.UpsertBranch(ctx, tx, branch)
				require.NoError(t, err, "should be able to upsert branch")
				branches[i] = &pfsdb.Branch{
					ID:         id,
					BranchInfo: branch,
				}
			}
			provenantBranches, err := pfsdb.GetProvenantBranches(ctx, tx, pfsdb.BranchID(size))
			require.NoError(t, err, "should be able to get branch info with provenance")
			require.Equal(t, len(provenantBranches), size-1)
			for _, branch := range provenantBranches {
				_, ok := branches[int(branch.ID)]
				require.True(t, ok, "found provenant branch should exist in map of created branches")
			}
			subvenantBranches, err := pfsdb.GetSubvenantBranches(ctx, tx, pfsdb.BranchID(1))
			require.NoError(t, err, "should be able to get branch info with subvenance")
			require.Equal(t, len(subvenantBranches), size-1)
			for _, branch := range subvenantBranches {
				_, ok := branches[int(branch.ID)]
				require.True(t, ok, "found subvenant branch should exist in map of created branches")
			}
			// test options
			maxDepth := uint64(5)
			provenantBranches, err = pfsdb.GetProvenantBranches(ctx, tx, pfsdb.BranchID(size), pfsdb.WithMaxDepth(maxDepth))
			require.NoError(t, err, "should be able to get branch info with provenance")
			require.Equal(t, len(provenantBranches), int(maxDepth))
			limit := uint64(2)
			subvenantBranches, err = pfsdb.GetSubvenantBranches(ctx, tx, pfsdb.BranchID(1), pfsdb.WithLimit(limit))
			require.NoError(t, err, "should be able to get branch info with provenance")
			require.Equal(t, len(subvenantBranches), int(limit))
		})
	})
}

func TestBranchProvenance(t *testing.T) {
	t.Parallel()
	withDB(t, func(ctx context.Context, t *testing.T, db *pachsql.DB) {
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			// Setup dependencies
			repoAInfo := newRepoInfo(&pfs.Project{Name: pfs.DefaultProjectName}, "A", pfs.UserRepoType)
			repoBInfo := newRepoInfo(&pfs.Project{Name: pfs.DefaultProjectName}, "B", pfs.UserRepoType)
			repoCInfo := newRepoInfo(&pfs.Project{Name: pfs.DefaultProjectName}, "C", pfs.UserRepoType)
			commitAInfo := newCommitInfo(repoAInfo.Repo, random.String(32), nil)
			commitBInfo := newCommitInfo(repoBInfo.Repo, random.String(32), nil)
			commitCInfo := newCommitInfo(repoCInfo.Repo, random.String(32), nil)
			for _, commitInfo := range []*pfs.CommitInfo{commitAInfo, commitBInfo, commitCInfo} {
				createCommit(t, ctx, tx, commitInfo)
			}
			// Create 3 branches, one for each repo, pointing to the corresponding commit
			branchAInfo := &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoAInfo.Repo,
					Name: "master",
				},
				Head: commitAInfo.Commit,
			}
			branchBInfo := &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoBInfo.Repo,
					Name: "master",
				},
				Head: commitBInfo.Commit,
			}
			branchCInfo := &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoCInfo.Repo,
					Name: "master",
				},
				Head: commitCInfo.Commit,
			}
			// Provenance info: A <- B <- C, and A <- C
			branchAInfo.Subvenance = []*pfs.Branch{branchBInfo.Branch, branchCInfo.Branch}
			branchBInfo.DirectProvenance = []*pfs.Branch{branchAInfo.Branch}
			branchBInfo.Provenance = []*pfs.Branch{branchAInfo.Branch}
			branchBInfo.Subvenance = []*pfs.Branch{branchCInfo.Branch}
			branchCInfo.DirectProvenance = []*pfs.Branch{branchAInfo.Branch, branchBInfo.Branch}
			branchCInfo.Provenance = []*pfs.Branch{branchBInfo.Branch, branchAInfo.Branch}
			// Create all branches, and provenance relationships
			allBranches := make(map[string]pfsdb.Branch)
			for _, branchInfo := range []*pfs.BranchInfo{branchAInfo, branchBInfo, branchCInfo} {
				id, err := pfsdb.UpsertBranch(ctx, tx, branchInfo) // implicitly creates prov relationships
				require.NoError(t, err)
				allBranches[branchInfo.Branch.Key()] = pfsdb.Branch{ID: id, BranchInfo: branchInfo}
			}
			// Verify direct provenance, full provenance, and full subvenance relationships
			for _, branch := range allBranches {
				id := branch.ID
				branchInfo := branch.BranchInfo
				gotDirectProv, err := pfsdb.GetDirectBranchProvenance(ctx, tx, id)
				require.NoError(t, err)
				require.NoDiff(t, branchInfo.DirectProvenance, gotDirectProv, compareBranchOpts())
				gotProv, err := pfsdb.GetFullBranchProvenance(ctx, tx, id)
				require.NoError(t, err)
				require.NoDiff(t, branchInfo.Provenance, gotProv, compareBranchOpts())
				gotSubv, err := pfsdb.GetFullBranchSubvenance(ctx, tx, id)
				require.NoError(t, err)
				require.NoDiff(t, branchInfo.Subvenance, gotSubv, compareBranchOpts())
			}
			// Update provenance DAG to A <- B -> C, to test adding and deleting prov relationships
			branchAInfo.DirectProvenance = nil
			branchAInfo.Provenance = nil
			branchAInfo.Subvenance = []*pfs.Branch{branchBInfo.Branch}
			branchBInfo.DirectProvenance = []*pfs.Branch{branchAInfo.Branch, branchCInfo.Branch}
			branchBInfo.Provenance = []*pfs.Branch{branchAInfo.Branch, branchCInfo.Branch}
			branchBInfo.Subvenance = nil
			branchCInfo.DirectProvenance = nil
			branchCInfo.Provenance = nil
			branchCInfo.Subvenance = []*pfs.Branch{branchBInfo.Branch}
			// The B -> C relationship causes a cycle, so need to update C first and remove the B <- C relationship.
			_, err := pfsdb.UpsertBranch(ctx, tx, branchBInfo)
			require.True(t, errors.As(err, &pfsdb.BranchProvCycleError{}))
			require.ErrorContains(t, err, "cycle detected")
			_, err = pfsdb.UpsertBranch(ctx, tx, branchCInfo)
			require.NoError(t, err)
			_, err = pfsdb.UpsertBranch(ctx, tx, branchAInfo)
			require.NoError(t, err)
			_, err = pfsdb.UpsertBranch(ctx, tx, branchBInfo)
			require.NoError(t, err)

			for _, branch := range allBranches {
				id := branch.ID
				branchInfo := branch.BranchInfo
				gotDirectProv, err := pfsdb.GetDirectBranchProvenance(ctx, tx, id)
				require.NoError(t, err)
				require.NoDiff(t, branchInfo.DirectProvenance, gotDirectProv, compareBranchOpts())
				gotProv, err := pfsdb.GetFullBranchProvenance(ctx, tx, id)
				require.NoError(t, err)
				require.NoDiff(t, branchInfo.Provenance, gotProv, compareBranchOpts())
				gotSubv, err := pfsdb.GetFullBranchSubvenance(ctx, tx, id)
				require.NoError(t, err)
				require.NoDiff(t, branchInfo.Subvenance, gotSubv, compareBranchOpts())
			}
		})
	})
}

func TestBranchIterator(t *testing.T) {
	t.Parallel()
	withDB(t, func(ctx context.Context, t *testing.T, db *pachsql.DB) {
		allBranches := make(map[pfsdb.BranchID]*pfs.BranchInfo)
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			// Create 2^8-1=255 branches
			headCommitInfo := newCommitInfo(&pfs.Repo{Project: &pfs.Project{Name: "test-project"}, Name: uuid.UniqueString("test-repo"), Type: pfs.UserRepoType}, random.String(32), nil)
			rootBranchInfo := &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: headCommitInfo.Commit.Repo,
					Name: "master",
				},
				Head: headCommitInfo.Commit,
			}

			currentLevel := []*pfs.BranchInfo{rootBranchInfo}
			for i := 0; i < 8; i++ {
				var newLevel []*pfs.BranchInfo
				for _, parent := range currentLevel {
					// create a commits and branches
					createCommit(t, ctx, tx, newCommitInfo(parent.Head.Repo, parent.Head.Id, nil))
					id, err := pfsdb.UpsertBranch(ctx, tx, parent)
					require.NoError(t, err)
					allBranches[id] = parent
					// Create 2 child for each branch in the current level
					for j := 0; j < 2; j++ {
						head := newCommitInfo(&pfs.Repo{Project: &pfs.Project{Name: "test-project"}, Name: uuid.UniqueString("test-repo"), Type: pfs.UserRepoType}, random.String(32), nil)
						child := &pfs.BranchInfo{
							Branch: &pfs.Branch{
								Repo: head.Commit.Repo,
								Name: "master",
							},
							Head: head.Commit,
						}
						newLevel = append(newLevel, child)
					}
				}
				currentLevel = newLevel
			}
		})
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			// List all branches
			branchIterator, err := pfsdb.NewBranchIterator(ctx, tx, 0 /* startPage */, 10 /* pageSize */, nil /* filter */)
			require.NoError(t, err)
			gotAllBranches := make(map[pfsdb.BranchID]*pfs.BranchInfo)
			require.NoError(t, stream.ForEach[pfsdb.Branch](ctx, branchIterator, func(branch pfsdb.Branch) error {
				gotAllBranches[branch.ID] = branch.BranchInfo
				require.Equal(t, allBranches[branch.ID].Branch.Key(), branch.BranchInfo.Branch.Key())
				require.Equal(t, allBranches[branch.ID].Head.Key(), branch.BranchInfo.Head.Key())
				return nil
			}))
			// Filter on a set of repos
			expectedRepoNames := []string{allBranches[1].Branch.Repo.Name}
			branchIterator, err = pfsdb.NewBranchIterator(ctx, tx, 0 /* startPage */, 10 /* pageSize */, allBranches[1].Branch, pfsdb.OrderByBranchColumn{Column: pfsdb.BranchColumnCreatedAt, Order: pfsdb.SortOrderAsc})
			require.NoError(t, err)
			var gotRepoNames []string
			require.NoError(t, stream.ForEach[pfsdb.Branch](ctx, branchIterator, func(branch pfsdb.Branch) error {
				gotRepoNames = append(gotRepoNames, branch.BranchInfo.Branch.Repo.Name)
				return nil
			}))
			require.Equal(t, len(expectedRepoNames), len(gotRepoNames))
			require.ElementsEqual(t, expectedRepoNames, gotRepoNames)
		})
	})
}

func TestBranchIteratorOrderBy(t *testing.T) {
	t.Parallel()
	withDB(t, func(ctx context.Context, t *testing.T, db *pachsql.DB) {
		var branches []*pfs.BranchInfo

		// Create two branches
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			repoInfo := newRepoInfo(&pfs.Project{Name: pfs.DefaultProjectName}, "repo1", pfs.UserRepoType)
			commitInfo := newCommitInfo(repoInfo.Repo, random.String(32), nil)
			createCommit(t, ctx, tx, commitInfo)
			// create first branch
			branchInfo := &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoInfo.Repo,
					Name: "master",
				},
				Head: commitInfo.Commit,
			}
			branches = append(branches, branchInfo)
			_, err := pfsdb.UpsertBranch(ctx, tx, branchInfo)
			require.NoError(t, err)
			// create second branch
			branchInfo = &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoInfo.Repo,
					Name: "staging",
				},
				Head: commitInfo.Commit,
			}
			branches = append(branches, branchInfo)
			_, err = pfsdb.UpsertBranch(ctx, tx, branchInfo)
			require.NoError(t, err)
		})

		// List all branches in reverse order
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			branchIterator, err := pfsdb.NewBranchIterator(ctx, tx, 0 /* startPage */, 10 /* pageSize */, nil /* filter */, pfsdb.OrderByBranchColumn{Column: pfsdb.BranchColumnID, Order: pfsdb.SortOrderDesc})
			require.NoError(t, err)
			var gotBranches []*pfs.BranchInfo
			require.NoError(t, stream.ForEach[pfsdb.Branch](ctx, branchIterator, func(branch pfsdb.Branch) error {
				gotBranches = append(gotBranches, branch.BranchInfo)
				return nil
			}))
			require.Equal(t, len(branches), len(gotBranches))
			for i := range branches {
				require.Equal(t, branches[i].Branch.Name, gotBranches[len(gotBranches)-1-i].Branch.Name)
			}
		})
	})
}

func TestBranchDelete(t *testing.T) {
	t.Parallel()
	withDB(t, func(ctx context.Context, t *testing.T, db *pachsql.DB) {
		var branchAInfo, branchBInfo, branchCInfo *pfs.BranchInfo
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			// Setup dependencies
			repoInfo := newRepoInfo(&pfs.Project{Name: pfs.DefaultProjectName}, "A", pfs.UserRepoType)
			commitAInfo := newCommitInfo(repoInfo.Repo, random.String(32), nil)
			commitBInfo := newCommitInfo(repoInfo.Repo, random.String(32), nil)
			commitCInfo := newCommitInfo(repoInfo.Repo, random.String(32), nil)
			for _, commitInfo := range []*pfs.CommitInfo{commitAInfo, commitBInfo, commitCInfo} {
				createCommit(t, ctx, tx, commitInfo)
			}
			// Create 3 branches, one for each repo, pointing to the corresponding commit
			branchAInfo = &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoInfo.Repo,
					Name: "branchA",
				},
				Head: commitAInfo.Commit,
			}
			branchBInfo = &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoInfo.Repo,
					Name: "branchB",
				},
				Head: commitBInfo.Commit,
			}
			branchCInfo = &pfs.BranchInfo{
				Branch: &pfs.Branch{
					Repo: repoInfo.Repo,
					Name: "branchC",
				},
				Head: commitCInfo.Commit,
			}
			// Provenance info: A <- B <- C, and A <- C
			branchAInfo.Subvenance = []*pfs.Branch{branchBInfo.Branch, branchCInfo.Branch}
			branchBInfo.DirectProvenance = []*pfs.Branch{branchAInfo.Branch}
			branchBInfo.Provenance = []*pfs.Branch{branchAInfo.Branch}
			branchBInfo.Subvenance = []*pfs.Branch{branchCInfo.Branch}
			branchCInfo.DirectProvenance = []*pfs.Branch{branchAInfo.Branch, branchBInfo.Branch}
			branchCInfo.Provenance = []*pfs.Branch{branchBInfo.Branch, branchAInfo.Branch}
			// Add a branch trigger to re-point branch C to B
			branchCInfo.Trigger = &pfs.Trigger{Branch: "branchB", CronSpec: "* * * * *"}
			for _, branchInfo := range []*pfs.BranchInfo{branchAInfo, branchBInfo, branchCInfo} {
				_, err := pfsdb.UpsertBranch(ctx, tx, branchInfo)
				require.NoError(t, err)
			}
			_, err := pfsdb.GetBranchID(ctx, tx, branchBInfo.Branch)
			require.NoError(t, err)
		})
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			// Delete branch should fail because there exists branches that depend on it.
			branchID, err := pfsdb.GetBranchID(ctx, tx, branchBInfo.Branch)
			require.NoError(t, err)
			branch := &pfsdb.Branch{ID: branchID, BranchInfo: branchBInfo}
			err = pfsdb.DeleteBranch(ctx, tx, branch, false /* force */)
			matchErr := fmt.Sprintf("branch %q cannot be deleted because it's in the direct provenance of %v",
				branchBInfo.Branch,
				[]*pfs.Branch{branchCInfo.Branch},
			)
			require.Equal(t, matchErr, err.Error())
		})
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			branchID, err := pfsdb.GetBranchID(ctx, tx, branchBInfo.Branch)
			require.NoError(t, err)
			branch := &pfsdb.Branch{ID: branchID, BranchInfo: branchBInfo}
			require.NoError(t, pfsdb.DeleteBranch(ctx, tx, branch, true /* force */))
			_, err = pfsdb.GetBranchInfo(ctx, tx, branchID)
			require.True(t, errors.As(err, &pfsdb.BranchNotFoundError{}))
			// Verify that BranchA no longer has BranchB in its subvenance
			branchAInfo.Subvenance = []*pfs.Branch{branchCInfo.Branch}
			branchAID, err := pfsdb.GetBranchID(ctx, tx, branchAInfo.Branch)
			require.NoError(t, err)
			gotBranchAInfo, err := pfsdb.GetBranchInfo(ctx, tx, branchAID)
			require.NoError(t, err)
			require.NoDiff(t, branchAInfo, gotBranchAInfo, compareBranchOpts())
			// Verify BranchC no longer has BranchB in its provenance, nor does it have the trigger
			branchCInfo.DirectProvenance = []*pfs.Branch{branchAInfo.Branch}
			branchCInfo.Provenance = []*pfs.Branch{branchAInfo.Branch}
			branchCInfo.Trigger = nil
			branchCID, err := pfsdb.GetBranchID(ctx, tx, branchCInfo.Branch)
			require.NoError(t, err)
			gotBranchCInfo, err := pfsdb.GetBranchInfo(ctx, tx, branchCID)
			require.NoError(t, err)
			require.NoDiff(t, branchCInfo, gotBranchCInfo, compareBranchOpts())
		})
	})
}

func TestBranchTrigger(t *testing.T) {
	t.Parallel()
	withDB(t, func(ctx context.Context, t *testing.T, db *pachsql.DB) {
		var masterBranchID, stagingBranchID pfsdb.BranchID
		// Create two branches, master and staging in the same repo.
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			var err error
			repoInfo := newRepoInfo(&pfs.Project{Name: "project1"}, "repo1", pfs.UserRepoType)
			commit1 := createCommit(t, ctx, tx, newCommitInfo(repoInfo.Repo, random.String(32), nil)).CommitInfo.Commit
			masterBranchInfo := &pfs.BranchInfo{Branch: &pfs.Branch{Repo: repoInfo.Repo, Name: "master"}, Head: commit1}
			masterBranchID, err = pfsdb.UpsertBranch(ctx, tx, masterBranchInfo)
			require.NoError(t, err)
			commit2 := createCommit(t, ctx, tx, newCommitInfo(repoInfo.Repo, random.String(32), nil)).CommitInfo.Commit
			stagingBranchInfo := &pfs.BranchInfo{Branch: &pfs.Branch{Repo: repoInfo.Repo, Name: "staging"}, Head: commit2}
			stagingBranchID, err = pfsdb.UpsertBranch(ctx, tx, stagingBranchInfo)
			require.NoError(t, err)
		})
		// Create the branch trigger that points master to staging.
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			trigger := &pfs.Trigger{
				Branch:        "staging",
				CronSpec:      "* * * * *",
				RateLimitSpec: "",
				Size:          "100M",
				Commits:       10,
				All:           true,
			}
			masterBranchInfo, err := pfsdb.GetBranchInfo(ctx, tx, masterBranchID)
			require.NoError(t, err)
			masterBranchInfo.Trigger = trigger
			_, err = pfsdb.UpsertBranch(ctx, tx, masterBranchInfo)
			require.NoError(t, err)
			masterBranchInfo, err = pfsdb.GetBranchInfo(ctx, tx, masterBranchID)
			require.NoError(t, err)
			require.Equal(t, trigger, masterBranchInfo.Trigger)
			// Update the trigger through UpsertBranchTrigger
			trigger = &pfs.Trigger{Branch: "staging", CronSpec: "0 * * * *", All: false}
			masterBranchInfo.Trigger = trigger
			_, err = pfsdb.UpsertBranch(ctx, tx, masterBranchInfo)
			require.NoError(t, err)
			masterBranchInfo, err = pfsdb.GetBranchInfo(ctx, tx, masterBranchID)
			require.NoError(t, err)
			require.Equal(t, trigger, masterBranchInfo.Trigger)
			// Delete branch trigger, and try to get it back via GetBranchInfo
			masterBranchInfo.Trigger = nil
			_, err = pfsdb.UpsertBranch(ctx, tx, masterBranchInfo)
			require.NoError(t, err)
			masterBranchInfo, err = pfsdb.GetBranchInfo(ctx, tx, masterBranchID)
			require.NoError(t, err)
			require.Nil(t, masterBranchInfo.Trigger)
			// staging branch shouldn't get a trigger
			stagingBranchInfo, err := pfsdb.GetBranchInfo(ctx, tx, stagingBranchID)
			require.NoError(t, err)
			require.Nil(t, stagingBranchInfo.Trigger)
			// Attempt to create trigger with nonexistent branch via UpsertBranch
			masterBranchInfo.Trigger = &pfs.Trigger{Branch: "nonexistent"}
			_, err = pfsdb.UpsertBranch(ctx, tx, masterBranchInfo)
			require.True(t, errors.As(err, &pfsdb.BranchNotFoundError{}))
			// Recreate the trigger for downstream test cases.
			masterBranchInfo.Trigger = &pfs.Trigger{Branch: "staging"}
			_, err = pfsdb.UpsertBranch(ctx, tx, masterBranchInfo)
			require.NoError(t, err)
		})
		// Try to delete the staging branch, which should fail because master depends on it for triggering.
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			stagingBranchInfo, err := pfsdb.GetBranchInfo(ctx, tx, stagingBranchID)
			require.NoError(t, err)
			masterBranchInfo, err := pfsdb.GetBranchInfo(ctx, tx, masterBranchID)
			require.NoError(t, err)
			err = pfsdb.DeleteBranch(ctx, tx,
				&pfsdb.Branch{ID: stagingBranchID, BranchInfo: stagingBranchInfo},
				false /* force */)
			require.YesError(t, err)
			msg := fmt.Sprintf("%q cannot be deleted because it is triggered by branches %v", stagingBranchInfo.Branch, []*pfs.Branch{masterBranchInfo.Branch})
			require.ErrorContains(t, err, msg)
		})
		// Delete with force should work.
		withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
			stagingBranchInfo, err := pfsdb.GetBranchInfo(ctx, tx, stagingBranchID)
			require.NoError(t, err)
			require.NoError(t, pfsdb.DeleteBranch(ctx, tx, &pfsdb.Branch{ID: stagingBranchID, BranchInfo: stagingBranchInfo}, true /* force */))
		})
	})
}

func testBranchPicker() *pfs.BranchPicker {
	return &pfs.BranchPicker{
		Picker: &pfs.BranchPicker_Name{
			Name: &pfs.BranchPicker_BranchName{
				Name: "test-branch",
				Repo: testRepoPicker(),
			},
		},
	}
}

func TestPickBranch(t *testing.T) {
	t.Parallel()
	namePicker := testBranchPicker()
	badBranchPicker := proto.Clone(namePicker).(*pfs.BranchPicker)
	badBranchPicker.GetName().Name = "does not exist"
	ctx := pctx.TestContext(t)
	db := newTestDB(t, ctx)
	withTx(t, ctx, db, func(ctx context.Context, tx *pachsql.Tx) {
		repoInfo := newRepoInfo(&pfs.Project{Name: pfs.DefaultProjectName}, testRepoName, testRepoType)
		commit := createCommit(t, ctx, tx, newCommitInfo(repoInfo.Repo, random.String(32), nil))
		branchInfo := &pfs.BranchInfo{
			Branch: &pfs.Branch{
				Repo: repoInfo.Repo,
				Name: "test-branch",
			},
			Head: commit.CommitInfo.Commit,
		}
		id, err := pfsdb.UpsertBranch(ctx, tx, branchInfo)
		require.NoError(t, err, "should be able to upsert branch")
		got, err := pfsdb.PickBranch(ctx, namePicker, tx)
		require.NoError(t, err, "should be able to pick branch")
		require.Equal(t, id, got.ID)
		_, err = pfsdb.PickBranch(ctx, nil, tx)
		require.YesError(t, err, "pick branch should error with a nil picker")
		_, err = pfsdb.PickBranch(ctx, badBranchPicker, tx)
		require.YesError(t, err, "pick branch should error with bad picker")
		require.True(t, errors.As(err, &pfsdb.BranchNotFoundError{}))
	})
}
