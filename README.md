[![REUSE status](https://api.reuse.software/badge/github.com/openmcp-project/service-provider-template)](https://api.reuse.software/info/github.com/openmcp-project/service-provider-template)

# service-provider-template

## Quality Criteria

<!-- Update the tier badge and tick each criterion as you implement it. See https://open-control-plane.io/developers/serviceprovider/quality-criteria for definitions. -->

[![Quality: Experimental](https://img.shields.io/badge/Quality-Experimental-e69138?style=flat-square&labelColor=555)](https://open-control-plane.io/developers/serviceprovider/quality-criteria)

| Criterion                         | Status  | Notes |
| --------------------------------- | :----:  | ----- |
| Deletion behaviour                |   ❌    |       |
| Status reporting & error messages |   ❌    |       |
| Operation annotations             |   ❌    |       |
| API stability policy              |   ❌    |       |
| Custom CA support                 |   ❌    |       |
| Release artifacts (image + OCM)   |   ❌    |       |
| Testing                           |   ❌    |       |
| Ownership and maintenance docs    |   ❌    |       |

See the [OpenControlPlane Quality Criteria](https://open-control-plane.io/developers/serviceprovider/quality-criteria) for definitions.

## About this project

A template for building @openmcp-project Service Providers.

## Requirements and Setup

1. Create a new repository based on this template.
2. Execute the template to create a new `ServiceProvider`.
3. Test your `ServiceProvider`.

The template includes a basic code generation command that lets you create a `ServiceProvider` for your Go module, API kind and group.
You can also choose to add sample code to get a fully functional `ServiceProvider`.

For a complete usage overview with the default settings, run:

```shell
go run ./cmd/template -h
```

Then execute the template, for example:

```shell
go run ./cmd/template -module github.com/yourorg/yourrepo -kind YourKind -group yourgroup
```

Running End-to-End tests:

```shell
task test-e2e
```

For a detailed guide on setup and usage, please refer to the full [Service Provider Development Guide](https://openmcp-project.github.io/docs/developers/serviceprovider/service-providers).

## CLI Flags

### Template Generator Flags

The template generator (`cmd/template`) supports the following flags:

- `-module`: Go module path (default: `github.com/openmcp-project/service-provider-template`)
- `-kind`: GVK kind name (default: `FooService`)
- `-group`: GVK group prefix, will be suffixed with `services.open-control-plane.io` (default: `foo`)
- `-v`: Generate with sample code (default: `false`)
- `-w`: Generate a service provider that reconciles its `DomainServiceAPI` on the [WorkloadCluster](https://openmcp-project.github.io/docs/about/design/service-provider#deployment-model) (default: `false`)
- `-s`: Generate secret watcher implementation (default: `false`)

### Service Provider Runtime Flags

The generated service provider supports the following runtime flags:

- `--verbosity`: Logging verbosity level (see [controller-runtime logging](https://github.com/kubernetes-sigs/controller-runtime/blob/main/TMP-LOGGING.md))
- `--environment`: Name of the environment (required for operation)
- `--provider-name`: Name of the provider resource (required for operation)
- `--metrics-bind-address`: Address for the metrics endpoint (default: `0`, use `:8443` for HTTPS or `:8080` for HTTP)
- `--health-probe-bind-address`: Address for health probe endpoint (default: `:8081`)
- `--leader-elect`: Enable leader election for controller manager (default: `false`)
- `--metrics-secure`: Serve metrics endpoint securely via HTTPS (default: `true`)
- `--enable-http2`: Enable HTTP/2 for metrics and webhook servers (default: `false`)

For a complete list of available flags, run the generated binary with `-h` or `--help`.

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://github.com/openmcp-project/service-provider-template/issues). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](https://github.com/openmcp-project/.github/blob/main/CONTRIBUTING.md).

## Security / Disclosure

If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/openmcp-project/service-provider-template/security/policy) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/openmcp-project/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright OpenControlPlane contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/openmcp-project/service-provider-template).

---

<p align="center">
  <a href="https://apeirora.eu/content/projects/">
    <img alt="BMWK-EU funding logo" src="https://apeirora.eu/assets/img/BMWK-EU.png" width="300"/>
  </a>
</p>

<p align="center">
  OpenControlPlane is part of <a href="https://apeirora.eu/content/projects/">ApeiroRA</a>, an EU Important Project of Common European Interest (IPCEI-CIS).
</p>

<p align="center">
  Copyright Linux Foundation Europe. For web site terms of use, trademark policy and other project policies please see <a href="https://linuxfoundation.eu/en/policies">https://linuxfoundation.eu/en/policies</a>.
</p>
