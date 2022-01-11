### General information
A Conduit release has the following parts:
* a GitHub release, which further includes
  * packages for different operating systems and architectures
  * a file with checksums for the packages
  * a changelog
  * the source code
* a GitHub package, which is the official Docker image for Conduit. It's available on GitHub's Container Registry.

### How to release a new version?
A release is triggered by pushing a new tag which starts with `v` (for example `v1.2.3`). Everything else is then handled by 
GoReleaser and GitHub actions. To push a new tag, please use the script [tag.sh](https://github.com/ConduitIO/conduit/blob/main/scripts/tag.sh), 
which also checks if the version conforms to SemVer.
