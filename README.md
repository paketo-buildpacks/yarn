# Yarn Cloud Native Buildpack

The Yarn CNB provides the [Yarn Package manager](https://yarnpkg.com). The
buildpack installs `yarn` onto the `$PATH` which makes it available for
subsequent buildpacks and/or in the final running container. An example of
buildpack that might use yarn is the [Yarn Install
CNB](https://github.com/paketo-buildpacks/yarn-install)

## Integration

The Yarn CNB provides `yarn` as dependency. Downstream buildpacks, like [Yarn
Install CNB](https://github.com/paketo-buildpacks/yarn-install) can require the
`yarn` dependency by generating a [Build Plan
TOML](https://github.com/buildpacks/spec/blob/master/buildpack.md#build-plan-toml)
file that looks like the following:

```toml
[[requires]]

  # The name of the Yarn dependency is "yarn". This value is considered
  # part of the public API for the buildpack and will not change without a plan
  # for deprecation.
  name = "yarn"

  # The version of the Yarn dependency is not required. In the case it
  # is not specified, the buildpack will provide the default version, which can
  # be seen in the buildpack.toml file.
  # If you wish to request a specific version, the buildpack supports
  # specifying a semver constraint in the form of "1.*", "1.22.*", or even
  # "1.22.4".
  version = "1.22.4"

  # The Yarn buildpack supports some non-required metadata options.
  [requires.metadata]

    # Setting the build flag to true will ensure that the yarn
    # depdendency is available on the $PATH for subsequent buildpacks during
    # their build phase. If you are writing a buildpack that needs to run yarn
    # during its build process, this flag should be set to true.
    build = true

    # Setting the launch flag to true will ensure that the yarn
    # dependency is available on the $PATH for the running application. If you are
    # writing an application that needs to run yarn at runtime, this flag should
    # be set to true.
    launch = true
```

## Usage

To package this buildpack for consumption:

```
$ ./scripts/package.sh --version <version-number>
```

This will create a `buildpackage.cnb` file under the `build` directory which you
can use to build your app as follows:
```
pack build <app-name> -p <path-to-app> -b <path/to/node-engine.cnb> -b build/buildpackage.cnb \
-b <path/to/node-and-yarn-requiring-cnb>
```

Though the API of this buildpack does not require `node`, yarn is unusable without node.

## `buildpack.yml` Configurations

There are no extra configurations for this buildpack based on `buildpack.yml`.

## Run Tests

To run all unit tests, run:
```
./scripts/unit.sh
```

To run all integration tests, run:
```
/scripts/integration.sh
```
