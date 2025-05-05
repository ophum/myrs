# myrs

[Design Doc](./docs/00_design.md)

## スクショ

### ログイン画面
![](./docs/images/sign-in-page.png)

### サイト作成画面
Gitリポジトリは`https://`で公開されている必要があります。
また、リポジトリの`www/{パス}`のディレクトリがデプロイされます。

![](./docs/images/site-create-page.png)

### サイトダッシュボード

サイトを作成した状態ではまだデプロイされていないため、デプロイを作成するボタンを押下する必要があります。
ログを閲覧する場合は、Logsボタンを押下します。

![](./docs/images/site-dashboard.png)


#### デプロイを作成する

最新のコミットを取得し `www/{パス}`の内容をコピーします。

![](./docs/images/deploy-create-page.png)


#### 有効にする

シンボリックリンクを作成し公開します。
　
![](./docs/images/deploy-active.png)

### デプロイされたWordPressにアクセスする

#### wordpressのセットアップ画面が表示される

![](./docs/images/wordpress-setup-page.png)


#### データベース情報を入力

サイトを作成するとサイト名のDBとユーザーが用意されており、auth_socketで認証するためパスワードなしで接続できます。

![](./docs/images/myrs-wp-setup-db.png)

#### ブログ情報を入力

![](./docs/images/myrs-wp-setup-info.png)

#### WordPressにログイン

![](./docs/images/myrs-wp-dashboard.png)

#### 記事を書く

![](./docs/images/myrs-wp-write-post.png)

#### 記事を見る

![](./docs/images/myrs-wp-view-post.png)

### ログを見る

サイトダッシュボードのLogsボタンからログページに遷移できます。
グラフは1分間のリクエスト数を１時間分表示しています。

![](./docs/images/view-log.png)