# Releasing Catpult

Releases are published via GitHub tags, which automatically trigger the GitHub Actions release workflow to compile cross-platform binaries and create a GitHub Release. The release is then propagated to the Homebrew tap (`quirkdom/homebrew-tap`) to generate pre-compiled bottles.

---

## 1. Prerequisites

1. Ensure all checks, linting, and tests pass:
   ```sh
   make check
   ```
2. Verify that the working tree on `master` is clean and pushed:
   ```sh
   git status
   git push origin master
   ```

---

## 2. Cut the GitHub Release

1. Create an annotated git tag (using semantic versioning, prefixed with `v`):
   ```sh
   git tag -s v0.1.0 -m "Release v0.1.0"
   ```
   *(If not using signed tags, `git tag -a v0.1.0 -m "Release v0.1.0"` works too.)*

2. Push the tag to GitHub:
   ```sh
   git push origin v0.1.0
   ```

3. Watch the Release workflow in the **Actions** tab of the `catpult` repository.
   - The workflow (`.github/workflows/release.yml`) compiles static binaries for all supported platforms:
     - `darwin/arm64`, `darwin/amd64`
     - `linux/amd64`, `linux/arm64`
     - `windows/amd64`, `windows/arm64`
   - It packages them into `.tar.gz` and `.zip` archives, generates `checksums.txt`, and creates the GitHub Release.

---

## 3. Update the Homebrew Tap (`quirkdom/homebrew-tap`)

Once the GitHub release tag is live:

### Option A: Manual Update (2 steps)

1. Compute the SHA256 checksum of the GitHub source archive:
   ```sh
   curl -sL https://github.com/quirkdom/catpult/archive/refs/tags/v0.1.0.tar.gz | shasum -a 256
   ```

2. In `quirkdom/homebrew-tap`, update [`Formula/catpult.rb`](https://github.com/quirkdom/homebrew-tap/blob/main/Formula/catpult.rb):
   - Update `url` to the new tag archive:
     ```ruby
     url "https://github.com/quirkdom/catpult/archive/refs/tags/v0.1.0.tar.gz"
     ```
   - Update `sha256` to the hash computed above.

3. Push to a branch and open a PR in `quirkdom/homebrew-tap`:
   ```sh
   cd ~/Developer/quirkdom-homebrew-tap
   git checkout -b bump-catpult-v0.1.0
   git add Formula/catpult.rb
   git commit -m "catpult 0.1.0"
   git push origin bump-catpult-v0.1.0
   # Open PR on GitHub
   ```

4. The tap's `tests.yml` workflow will automatically run `brew test-bot` across macOS and Linux runners to compile bottles and upload artifacts.

5. Once the tests succeed, trigger the bottle publication workflow:
   - Go to `quirkdom/homebrew-tap` → **Actions** → **brew pr-pull**.
   - Click **Run workflow** and enter the Pull Request number.
   - Homebrew's `pr-pull` automatically uploads the bottles to GitHub Packages (`ghcr.io`), inserts the `bottle do ... end` block into `Formula/catpult.rb`, and merges the PR.

---

### Option B: Automated Update (`brew bump`)

The tap repository includes a scheduled workflow (`.github/workflows/autobump.yml`) that runs daily:
- It discovers new releases from GitHub using `brew bump`.
- It automatically opens the bump Pull Request for you.
- You only need to review the PR and run the `brew pr-pull` workflow to bottle and merge it.
