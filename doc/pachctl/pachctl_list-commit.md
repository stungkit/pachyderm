## ./pachctl list-commit

Return all commits on a set of repos.

### Synopsis


Return all commits on a set of repos.

Examples:

```sh

# return commits in repo "foo"
$ pachctl list-commit foo

# return commits in repo "foo" on branch "master"
$ pachctl list-commit foo master

# return the last 20 commits in repo "foo" on branch "master"
$ pachctl list-commit foo master -n 20

# return commits that are the ancestors of XXX
$ pachctl list-commit foo XXX

# return commits in repo "foo" since commit XXX
$ pachctl list-commit foo master --from XXX

```

```
./pachctl list-commit repo-name
```

### Options

```
  -f, --from string   list all commits since this commit
  -n, --number int    list only this many commits; if set to zero, list all commits
      --raw           disable pretty printing, print raw json
```

### Options inherited from parent commands

```
      --no-metrics   Don't report user metrics for this command
  -v, --verbose      Output verbose logs
```

### SEE ALSO
* [./pachctl](./pachctl.md)	 - 

###### Auto generated by spf13/cobra on 1-May-2019