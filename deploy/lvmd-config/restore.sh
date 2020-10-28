#!/bin/bash

while getopts ":v:b:e:p:h:s:y:a:k:t:c:" arg; do
  case $arg in
    v) VOLUME_HANDLE=$OPTARG;;
    b) BACKUP_TYPE=$OPTARG;;
    e) ENDPOINT=$OPTARG;;
    p) BACKUP_PATH=$OPTARG;;
    h) HTTP_PROXY=$OPTARG;;
    s) HTTPS_PROXY=$OPTARG;;
    y) VERIFY_TLS=$OPTARG;;
    a) ACCESS_KEY_ID=$OPTARG;;
    k) SECRET_ACCESS_KEY=$OPTARG;;
    t) SESSION_TOKEN=$OPTARG;;
    c) ENCRYPTION_KEY=$OPTARG;;
  esac
done

echo \
${VOLUME_HANDLE} \
${BACKUP_TYPE} \
${ENDPOINT} \
${BACKUP_PATH} \
${HTTP_PROXY} \
${HTTPS_PROXY} \
${VERIFY_TLS} \
${ACCESS_KEY_ID} \
${SECRET_ACCESS_KEY} \
${SESSION_TOKEN} \
${ENCRYPTION_KEY} >> /var/log/restore.log

# /usr/local/bin/topolvm-restore.sh \
# -v ssd-vg/myvolume \
# -b S3 \
# -e https://blablub.de \
# -p /tmp/snap-mybackup-a4588384-c66c-4e62-b433-822a36b61f1c-20201023_231259.tar.gz \
# -h httpProxy \
# -s httpsProxy \
# -y false \
# -a admin \
# -k admin \
# -t admin \
# -c admin

#######################################################
######### Begin of VAR Section ########################
#######################################################

## backed service (e.g. backup, restore)
service="restore"

## log tag for search in journald
lvmdtag="lvmd-${service}"

## Workdir
workdir="/var/topolvm"

## LVM volume name without a slash (vg/lm -> vg-lv)
lv=$(echo ${VOLUME_HANDLE} | sed 's/\//-/g')

## FS type
fstype="xfs"

## Error log file
errlogfile="${workdir}/${service}/error/${lv}"

## Lock dir which acts as a signal for a running backup job
inprogressdir="${workdir}/${service}/inprogress/${lv}"

## Dir that signals backup job has been completed successfully
completedir="${workdir}/${service}/complete/${lv}"

## local lvm restore mount point
restoremp="/mnt/lvmd/${service}/${lv}"


#######################################################
######### End of VAR Section ##########################
#######################################################

## Global accessible message var
msg=""

# Esure base dir hirarchy is present
mkdir -p "${workdir}/${service}/error"
mkdir -p "${workdir}/${service}/inprogress"
mkdir -p "${workdir}/${service}/complete"

cleanup() {
  {
    ## unmount lv
    if mountpoint -q "${restoremp}"
    then
      umount "${restoremp}" || umount --force "${restoremp}";
    fi

    ## Important: remove InProgress state/lock file
    rm -rf "${inprogressdir}"

    rm -rf "${restoremp}"
  } || {
    logger -p local0.error -t "${lvmdtag}" "Clean up failed please. Exit script.";
  }
}


## At the beginning we make sure only one restore process per lvm volume is running at the same time
if ! mkdir "${inprogressdir}"
then
  msg="Already running restore job for \"${VOLUME_HANDLE}\" found. Stop restoreing. Exit script.";
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  echo "${msg}" >> "${errlogfile}";
  exit 0;
fi

# Check what filesystem has been placed at target volume
#fstype=$(blkid /dev/${VOLUME_HANDLE} | sed 's/.*TYPE="//g' | sed 's/".*//g')

# Create Filesystem
mkfs -t ${fstype} /dev/${VOLUME_HANDLE}

## create restore mount point if not present
mkdir -p "${restoremp}";

## mount just created snapshot
if [ "$fstype" =  "xfs" ]
then
msg="$(mount -o nouuid -t ${fstype} /dev/${VOLUME_HANDLE} ${restoremp} 2>&1)";
else
msg="$(mount -t ${fstype} /dev/${VOLUME_HANDLE} ${restoremp} 2>&1)";
fi
if [ $? -ne 0 ]
then
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  echo "${msg}" >> "${errlogfile}"
  cleanup;
  exit 1;
else
  logger -p local0.info -t "${lvmdtag}" "LV mounted for restoring data to \"${VOLUME_HANDLE}\"";
fi


## extract entire content of tarball (backup) to lv mounted on "restoremp"
msg="$(tar -xf ${BACKUP_PATH} -C ${restoremp} . 2>&1)";
if [ $? -ne 0 ]
then
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  echo "${msg}" >> "${errlogfile}"
  cleanup;
  exit 1;
else
  logger -p local0.info -t "${lvmdtag}" "Restoring backup into \"${VOLUME_HANDLE}\" done.";
  mkdir -p "${completedir}";
  cleanup;
fi
