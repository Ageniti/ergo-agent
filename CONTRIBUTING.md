# Contributing

Thank you for your interest in Ergo Agent.

## Contributor licensing

This project uses an AGPL/commercial dual-license model. To preserve the
project's ability to offer both license options, external contributions are
accepted only after the contributor has signed a separate Contributor License
Agreement (CLA) granting the project owner the rights required to distribute
the contribution under both open-source and commercial terms.

Opening a pull request does not by itself transfer copyright or grant
commercial relicensing rights. Maintainers must not merge an external
contribution until the CLA status has been recorded.

Contributors must have the legal right to submit their work and must identify
any third-party code, generated material, or license restrictions included in
the contribution.

For CLA and contribution enquiries, contact
[yiliu.li@outlook.com](mailto:yiliu.li@outlook.com).

## Quality checks

Before submitting a change, run:

```bash
go test ./...
go test -race -count=1 ./...
go vet ./...
staticcheck ./...
govulncheck ./...
```
