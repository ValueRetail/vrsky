# #387: the Windows ENCRYPTION_KEY snippet uses a non-cryptographic RNG

Backlog item 33, found in review of #381.

## Problem

`docs/operator/windows.md` tells Windows users to generate `ENCRYPTION_KEY`
with:

```powershell
-join ((1..32) | ForEach-Object { '{0:x2}' -f (Get-Random -Maximum 256) })
```

`Get-Random` is built on `System.Random`, a seeded PRNG. The key encrypts
every stored connector credential, so it has to come from a CSPRNG, like
`openssl rand -hex 32` does on macOS and Linux.

## What the code accepts

`src/pkg/crypto/secretbox.go`:

- line 32: `ErrKeyMissing` says "64 hex characters (32 bytes)"
- lines 124-136 (`decodeKey`): `hex.DecodeString` (line 128), then
  `len(key) != 32` is rejected (line 132).
  Go's hex decoder takes upper or lower case; anything else, or another
  length, is rejected.

The Unix docs (`docs/operator/install.md:34`, `README.md:30`, `.env.example`)
use `openssl rand -hex 32`: 64 lowercase hex characters. The new snippet
produces the same.

## New snippet

```powershell
$bytes = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes)
$rng.Dispose()
-join ($bytes | ForEach-Object { '{0:x2}' -f $_ })
```

Why this shape:

- `RandomNumberGenerator::Create()` plus the instance `GetBytes(byte[])`
  exist in .NET Framework 2.0 onward and in .NET Core/5+, so it runs on
  Windows PowerShell 5.1 (.NET Framework 4.x) and PowerShell 7. The static
  `RandomNumberGenerator::GetBytes(int)` and `Convert.ToHexString` are .NET
  Core/5+ only and are avoided.
- `New-Object byte[] 32` rather than `[byte[]]::new(32)`: both work on 5.1,
  `New-Object` reads more plainly to someone who doesn't write PowerShell.
- `Dispose()` exists on `RandomNumberGenerator` since .NET Framework 4.0.
- `'{0:x2}'` keeps the lowercase output of `openssl rand -hex`.

The doc also says that this is the PowerShell equivalent of
`openssl rand -hex 32`, and that `openssl rand -hex 32` works as is from WSL
or Git Bash.

## Other docs

Searched the repo (outside `node_modules` and `.git`) for `Get-Random`,
`System.Random`, `$RANDOM`, `RandomNumberGenerator`. The only secret built
from a weak RNG is this snippet. `$RANDOM` appears in
`test/test-file-pipeline-docker.sh` and `tests/load/*` to make file and
pipeline names unique, not secrets; left alone. Every other secret in docs
and scripts uses `openssl rand`.

## Files

- `docs/operator/windows.md`
- `plans/backlog.md` (strike item 33)
- this plan

## Verification

- PowerShell 7: run the snippet, extracted straight from `windows.md`, in
  Docker on macOS and pipe the output into a throwaway Go program that calls
  the real `crypto.Key()` from `src/pkg/crypto` and does an encrypt/decrypt
  round trip. The key itself is not printed or kept.
  `mcr.microsoft.com/powershell:latest` only has a 32-bit ARM build for this
  Mac and crashed under qemu, so the run used the arm64
  `mcr.microsoft.com/dotnet/sdk:8.0` image (PowerShell 7.4.21, .NET 8.0.33).
- PowerShell 5.1: no Windows machine available, so this is reasoned from the
  .NET Framework API surface above, not run. The snippet uses no PS 6+/7
  syntax (no `??`, ternary, `::new` or pipeline chain operators).
- Run the snippet 20 times and check the outputs differ. The checker gets a
  negative control too (short, non-hex and 62-character input must fail).
- `mkdocs build --strict` in a scratch venv with mkdocs-material, outside the
  repo.
