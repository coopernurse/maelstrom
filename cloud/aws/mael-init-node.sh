#!/bin/bash -xe

role="maelnode"

EC2_AVAIL_ZONE=`curl -s http://169.254.169.254/latest/meta-data/placement/availability-zone`
EC2_INSTANCE_ID=`curl -s http://169.254.169.254/latest/meta-data/instance-id`
EC2_REGION="`echo \"$EC2_AVAIL_ZONE\" | sed 's/[a-z]$//'`"

# create systemd unit
cat <<EOF > /etc/systemd/system/maelstromd.service
[Unit]
Description=maelstromd
After=docker.service
[Service]
TimeoutStartSec=0
Restart=always
RestartSec=5
Environment=AWS_REGION=${EC2_REGION}
Environment=MAEL_INSTANCE_ID=${EC2_INSTANCE_ID}
Environment=MAEL_SQL_DRIVER=${MAEL_SQL_DRIVER}
Environment=MAEL_SQL_DSN=${MAEL_SQL_DSN}
Environment=MAEL_AWS_TERMINATE_QUEUE_URL=${MAEL_AWS_TERMINATE_QUEUE_URL}
Environment=MAEL_SHUTDOWN_PAUSE_SECONDS=5
ExecStartPre=/bin/mkdir -p /var/maelstrom
ExecStartPre=/bin/chmod 700 /var/maelstrom
ExecStart=/usr/bin/maelstromd
[Install]
WantedBy=multi-user.target
EOF
chmod 600 /etc/systemd/system/maelstromd.service

# set hostname
hostname="${role}-${EC2_INSTANCE_ID}"
sudo hostname ${hostname}
sudo bash -c "echo ${hostname} > /etc/hostname"

# start docker
systemctl restart docker

# start maelstromd
systemctl daemon-reload
systemctl enable maelstromd
systemctl start maelstromd
