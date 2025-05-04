# git archiveを試す

gitリポジトリから.gitなどを省いたファイルのアーカイブを作成する機能があります。
この動作について調べる。

```
t-inagaki@x1carbon:~/github.com/ophum/myrs$ git archive -h
usage: git archive [<options>] <tree-ish> [<path>...]
   or: git archive --list
   or: git archive --remote <repo> [--exec <cmd>] [<options>] <tree-ish> [<path>...]
   or: git archive --remote <repo> [--exec <cmd>] --list

    --format <fmt>        archive format
    --prefix <prefix>     prepend prefix to each pathname in the archive
    --add-file <file>     add untracked file to archive
    -o, --output <file>   write the archive to this file
    --worktree-attributes
                          read .gitattributes in working directory
    -v, --verbose         report archived files on stderr
    -NUM                  set compression level

    -l, --list            list supported archive formats

    --remote <repo>       retrieve the archive from remote repository <repo>
    --exec <command>      path to the remote git-upload-archive command

```

対応しているアーカイブ形式を確認
```
t-inagaki@x1carbon:~/github.com/ophum/myrs$ git archive --list
tar
tgz
tar.gz
zip
```

とりあえずローカルのgitリポジトリでやってみる
```
t-inagaki@x1carbon:~/github.com/ophum/myrs$ git archive -o archive.tar.gz HEAD
```

うまくアーカイブができていることが分かる。
```
t-inagaki@x1carbon:~/github.com/ophum/myrs$ tar -tf archive.tar.gz 
.gitignore
README.md
bin/
bin/.gitignore
deploy.sh
docs/
docs/00_design.md
docs/10_nginx-php-fpm.md
docs/20_mysql.md
docs/images/
docs/images/default-phpinfo-user.png
docs/images/default-phpinfo.png
docs/images/isolation.png
docs/images/need-mysqli.png
docs/images/topo.png
docs/images/user-a.example.com-phpinfo-user.png
docs/images/user-a.example.com-phpinfo.png
docs/images/wordpress-installed.png
docs/images/wordpress-setup-db.png
docs/images/wordpress-setup-run.png
docs/images/wordpress-setup-site-settings-need-mail.png
docs/images/wordpress-setup-site-settings.png
docs/images/wordpress-setup.png
docs/images/wordpress-signed-in.png
docs/images/wordpress-view-post.png
docs/images/wordpress-write-post.png
go.mod
go.sum
internal/
internal/utils.go
main.go
nginx-site.conf.tmpl
php-fpm-pool.conf.tmpl
templates/
templates/create-site.html
templates/index.html
```

次にremoteを試す
対応していないっぽい？
```
t-inagaki@x1carbon:~/github.com/ophum/myrs$ git archive --remote git@github.com:ophum/myrs.git -o archvie-remote.tar.gz HEAD
Invalid command: git-upload-archive 'ophum/myrs.git'
  You appear to be using ssh to clone a git:// URL.
  Make sure your core.gitProxy config option and the
  GIT_PROXY_COMMAND environment variable are NOT set.
fatal: the remote end hung up unexpectedly
```

普通にcloneして、wwwディレクトリをコピーするという仕様にすればよい気がした。