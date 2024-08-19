# Pipeline Configuration Files

## Summary

This document describes the behavior of provisioning pipelines using pipeline
configuration files.

## Context

We want to provide a way to provision pipelines using configuration files in
addition to the API. This unlocks the ability to treat pipelines like other
configurations and store them in a repository, version them, do code reviews on
changes, deploy, test etc. Doing the same thing through the API is error-prone
and tedious.

## Decision

Pipeline configuration files are loaded at the start of Conduit. The end result
is a provisioned pipeline that matches whatever is defined in the configuration
files (as long as the configuration files are valid).

In a pipeline configuration file no changes are forbidden, configuration files
are treated as the source of truth. This means that you can change a
configuration file between Conduit runs in any way you want, knowing that after
the start Conduit will load the structure in the new configuration file. The
worst thing that can happen is that you make an "invalid change" (e.g. you
change the source connector plugin) and the state of that entity gets erased.
That shouldn't be unexpected though, because you changed the plugin anyway, so
you can't really expect to start from where you left off.

Entities created through configuration files are stored in the Conduit store so
that state can be preserved between runs.

If a pipeline configuration file is loaded and we detect that Conduit already
contains that pipeline in the store, the existing pipeline is updated to match
whatever is in the configuration file.

A conflict can happen if the configuration file contains a change that we
normally wouldn't allow through the API (e.g. the connector plugin was changed).
In that case we log a warning about the invalid change, delete the entity that
contains the invalid change and recreate it from scratch. The consequence here
is that any state of that entity will be deleted (e.g. starting position of the
source connector).

When creating entities we mark them with a flag if they are created from a
configuration file - if they are not found in a configuration file at startup,
those entities get deleted. Entities that were provisioned through a
configuration file also can't be mutated through the API, they need to be
changed in the corresponding configuration file.

Files that contain multiple pipelines with the same ID (duplicates) are treated
as invalid and are skipped. If pipelines with the same ID are defined in
multiple files, the first pipeline will be provisioned and all other will be
skipped.

Configuration files are treated separately, meaning that one invalid file won't
cause other configuration files from being provisioned.

## Consequences

This approach makes configuration files the single source of truth for pipelines
provisioned through configuration files (whatever is in the configuration files
will be in Conduit after start). It also ensures the state is preserved when it
can be ( only incompatible changes cause the state to be deleted).

## Related

[Pipeline configuration files documentation](https://conduit.io/docs/pipeline-configuration-files)
