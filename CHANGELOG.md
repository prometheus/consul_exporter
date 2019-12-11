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
