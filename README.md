# kcp API Sync Agent

[![Go Report Card](https://goreportcard.com/badge/github.com/kcp-dev/api-syncagent)](https://goreportcard.com/report/github.com/kcp-dev/api-syncagent)
[![GitHub](https://img.shields.io/github/license/kcp-dev/api-syncagent)](https://github.com/kcp-dev/api-syncagent/blob/main/LICENSE)
[![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/kcp-dev/api-syncagent?sort=semver)](https://github.com/kcp-dev/api-syncagent/releases/latest)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkcp-dev%2Fapi-syncagent.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkcp-dev%2Fapi-syncagent?ref=badge_shield)

The kcp API Sync Agent is a Kubernetes controller capable of synchronizing objects from many kcp
workspaces onto a single Kubernetes cluster (with kcp being the source of truth). In doing so it will
move the desired state (usually the spec) of an object from kcp to the local cluster where the agent
is running, and move the current object status back up into kcp. The agent can also sync so-called
related objects, like a Secret belonging to a Certificate, in both directions.

The agent can be used to provide an API in kcp and then serving it from a remote Kubernetes cluster
where the actual workload is then processed, usually by a 3rd-party operator. In many situations the
synchronized objects are further processed using tools like Crossplane.

## Documentation

Please visit [https://docs.kcp.io/api-syncagent](https://docs.kcp.io/api-syncagent) for the latest
documentation.

## Troubleshooting

If you encounter problems, please [file an issue][1].

## Contributing

Thanks for taking the time to start contributing!

### Before you start

* Please familiarize yourself with the [Code of Conduct][4] before contributing.
* See [CONTRIBUTING.md][2] for instructions on the developer certificate of origin that we require.

### Pull requests

* We welcome pull requests. Feel free to dig through the [issues][1] and jump in.

## Changelog

See [the list of releases][3] to find out about feature changes.

## License

Apache 2.0

[1]: https://github.com/kcp-dev/api-syncagent/issues
[2]: https://github.com/kcp-dev/api-syncagent/blob/main/CONTRIBUTING.md
[3]: https://github.com/kcp-dev/api-syncagent/releases
[4]: https://github.com/kcp-dev/api-syncagent/blob/main/CODE_OF_CONDUCT.md
