# Conduit as a Nomad service job. EXAMPLE, NOT A SUPPORTED PRODUCT (see ../README.md).
#
#   nomad job run conduit.nomad.hcl
#
# State lives on the allocation's sticky ephemeral disk, which Nomad migrates
# when the allocation is replaced. That survives reschedules, not the loss of
# every client holding the disk: for anything you cannot replay, use a Postgres
# state store (db.type=postgres, connection string from Nomad Variables) instead.
job "conduit" {
  datacenters = ["dc1"]
  type        = "service"

  group "conduit" {
    count = 1 # Conduit has no clustering: one instance owns its pipelines.

    ephemeral_disk {
      sticky  = true
      migrate = true
      size    = 500 # MB
    }

    network {
      port "http" {
        to = 8080
      }
    }

    update {
      # One instance and one state directory: stop the old one before starting
      # the new, never run two against the same state.
      max_parallel = 1
      auto_revert  = true
    }

    service {
      name     = "conduit"
      port     = "http"
      provider = "nomad"

      check {
        name     = "ready"
        type     = "http"
        path     = "/readyz"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "conduit" {
      driver = "docker"

      config {
        image   = "ghcr.io/conduitio/conduit:v0.20.4"
        ports   = ["http"]
        volumes = ["local/pipelines:/app/pipelines:ro"]
      }

      env {
        # /alloc/data is the allocation's shared, sticky data directory.
        CONDUIT_DB_TYPE        = "badger"
        CONDUIT_DB_BADGER_PATH = "/alloc/data/conduit.db"
        CONDUIT_LOG_FORMAT     = "json"
      }

      template {
        destination = "local/pipelines/pipeline.yaml"
        data        = <<-EOT
          version: "2.2"
          pipelines:
            - id: example
              status: running
              name: example
              connectors:
                - id: gen
                  type: source
                  plugin: builtin:generator
                  settings:
                    format.type: structured
                    format.options.id: int
                    operations: create
                    rate: "1"
                - id: log
                  type: destination
                  plugin: builtin:log
        EOT
      }

      # SIGTERM drains in-flight records and checkpoints; the runtime gives up
      # after 30 s, so wait longer than that before SIGKILL.
      kill_signal  = "SIGTERM"
      kill_timeout = "45s"

      resources {
        cpu    = 500
        memory = 512
      }
    }
  }
}
