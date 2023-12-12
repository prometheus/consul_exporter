## 0.10.0 / 2023-12-12

* [ENHANCEMENT] Add `--version` flag to print the version (#257)

## 0.9.0 / 2022-11-29

* [SECURITY] Update Exporter Toolkit (CVE-2022-46146) #250
* [FEATURE] Support multiple Listen Addresses and systemd socket activation #250

## 0.8.0 / 2022-02-07

* [FEATURE] Enable TLS/basic authentication #205
* [FEATURE] Add metric to collect wan status #212

## 0.7.1 / 2020-07-21

* [BUGFIX] Fix /metrics hanging when `--consul.request-limit` is lower than the number of services in Consul. #179

## 0.7.0 / 2020-06-11

* [FEATURE] Add `consul_service_checks` metric to link checks with their services. #162
* [ENHANCEMENT] Add `--consul.request-limit` flag to limit the maximum number of concurrent requests to Consul. #164

## 0.6.0 / 2019-12-11

* [CHANGE] Run as a non-root user in the container. #139
* [CHANGE] Increase the default timeout. #147
* [CHANGE] Switch logging to go-kit. The `log.level` flag can be one of `debug`, `info`, `warn` or `error`. The `--log.format` flag can be either `logfmt` (default) or `json`. #144
* [ENHANCEMENT] Add /-/healthy and /-/ready endpoints. #153
* [ENHANCEMENT] Expose agent members status with the `consul_serf_lan_member_status` metric. #130
* [ENHANCEMENT] Handle Consul errors in a consistent fashion. #145
* [BUGFIX] Fix potential label clashes. #142

Contributors:

* @Kerl1310
* @chrsblck
* @rk295
* @simonpasquier
* @soloradish
* @sowmiyamuthuraman
* @timkra
* @vsamidurai

## 0.5.0 / 2019-07-17

* [ENHANCEMENT] Add --consul.insecure flag to skip TLS verification. #99
