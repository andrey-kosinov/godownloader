
CREATE DATABASE IF NOT EXISTS downloader;

CREATE TABLE files (
	id int NOT NULL AUTO_INCREMENT,
	url varchar(512),
	md5 varchar(255),
	tries tinyint UNSIGNED NOT NULL DEFAULT 0,
	ok tinyint UNSIGNED NOT NULL DEFAULT 0,
	progress tinyint UNSIGNED NOT NULL DEFAULT 0,
	bitrate varchar(100),
	resolution varchar(100),
	created_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
	done_at timestamp NULL,
	error varchar(255),

	PRIMARY KEY(id)
);

GRANT ALL ON downloader.* TO downloader@localhost IDENTIFIED BY 'downloader';

