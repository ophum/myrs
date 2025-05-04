#!/bin/bash

go build -o ./bin/myrs main.go

ssh root@10.4.1.13 -- pkill myrs
scp ./bin/myrs root@10.4.1.13:~/myrs/myrs