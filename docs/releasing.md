# Releasing

The SDK is released as one Go module: `github.com/echocat/sendspin-player-go`.
Use semantic version tags such as `v0.1.0`; SDK versions are independent of the
Sendspin protocol specification version.

## Before publishing

1. Choose the intended release commit on `main`.
2. Run the local checks on that commit:

   ```shell
   mise check:full
   ```

3. Confirm that CI succeeded for the same commit and summarize the API changes.

## Publish

Create and publish a GitHub release with the chosen version tag targeting that
commit. No additional example-module tags or generated release commit are needed.
If a published release needs fixing, publish a new patch version; never move or
delete a published tag.
