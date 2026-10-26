# Third-party notices

Homebrew's API public key and version-ordering semantics are derived from
[Homebrew/brew](https://github.com/Homebrew/brew), licensed under BSD-2-Clause.
Its full notice is retained in `internal/catalog/HOMEBREW-LICENSE.txt` and included
in release archives. Homebrew formula metadata and software bottles remain the
work of their respective authors and retain their individual licenses.

gobrew's architecture was informed by zerobrewhq/zerobrew and justrach/nanobrew.
It does not bundle either project's executable or runtime.

Runtime Go dependencies are pinned in go.mod/go.sum:

- github.com/spf13/cobra and github.com/spf13/pflag: Apache-2.0 / BSD-3-Clause.
- go.etcd.io/bbolt: MIT.
- golang.org/x/sys and golang.org/x/sync: BSD-3-Clause.

See each dependency's source distribution for its full license notice.
