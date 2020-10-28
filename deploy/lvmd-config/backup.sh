#!/bin/bash

while getopts ":n:v:b:e:p:h:s:y:a:k:t:c:" arg; do
  case $arg in
    n) BACKUP_NAME=$OPTARG;;
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
${BACKUP_NAME} \
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
${ENCRYPTION_KEY} >> /var/log/backup.log

## Test
#/usr/local/bin/topolvm-backup.sh -n mybackup -v a4588384-c66c-4e62-b433-822a36b61f1c -b S3 -e https://blablub.de -p bla.tar.gz -h httpProxy -s httpsProxy -y false -a admin -k admin -t admin -c admin


#######################################################
######### Begin of VAR Section ########################
#######################################################

## snapshot size for taking diff into account:
## -> as we are creating cow snapshot we need to provide enough space to take 
## ongoing writes into account until snapshot gets dropped, if snapshot runs 
## out of space writes will fail for both snapshot and original lv!!!
##
## As snapshotting volumes comes with costs at I/O it's better to drop
## snapshot as soon as possible. In this script we are going to remove snapshot 
## immediately after archive has been created. So we need enough space to hold 
## writes during this time window.
##
## (But removing snapshot comes with downside of more complex and slow restore 
## procedure as we cannot make use of 'lvconvert --merge <snapshot>')
snapsize="10%ORIGIN"

## backed service (e.g. backup, restore)
service="backup"

## log tag for search in journald
lvmdtag="lvmd-${service}"

## local lvm snapshot mount point
snapmp="/mnt/lvmd/backup/${VOLUME_HANDLE}"

## declare snpashot name
snapname="snap-${BACKUP_NAME}-${VOLUME_HANDLE}"

## FS type
fstype="xfs"

## Workdir
workdir="/var/topolvm"

## Error log file
errlogfile="${workdir}/${service}/error/${BACKUP_NAME}-${VOLUME_HANDLE}"

## Lock dir which acts as a signal for a running backup job
inprogressdir="${workdir}/${service}/inprogress/${BACKUP_NAME}-${VOLUME_HANDLE}"

## Dir that signals backup job has been completed successfully
completedir="${workdir}/${service}/complete/${BACKUP_NAME}-${VOLUME_HANDLE}"



#######################################################
######### End of VAR Section ##########################
#######################################################

vg=""

mkdir -p "${workdir}/${service}/error"
mkdir -p "${workdir}/${service}/inprogress"
mkdir -p "${workdir}/${service}/complete"

cleanup() {
  {
    ## unmount snapshot
    if mountpoint -q "${snapmp}"
    then
      umount "${snapmp}" || umount --force "${snapmp}";
    fi

    if [ ! -z "$vg" ]
    then
    ## remove snapshot to unload I/O
      lvdisplay "/dev/${vg}/${snapname}" 2> /dev/null && \
      (lvremove -y "/dev/${vg}/${snapname}" || lvremove --force "/dev/${vg}/${snapname}");
    fi

    ## Important: remove InProgress state/lock file
    rm -rf "${inprogressdir}"

    rm -rf "${snapmp}"
  } || {
    logger -p local0.error -t "${lvmdtag}" "Clean up failed please see logs in ${errlogfile} . Exit script.";
  }
}

## If script is called after a successfull run but before lvmd has recognized it could be run again
## so we need to stop if there is a dir under .../backup/complete/...
if [ -f "$completedir" ]; 
then
    exit 0
fi

## At the beginning we make sure only one backup process per volume is running at the same time
if ! mkdir "${inprogressdir}"
then
  msg="Already running backup job for \"${VOLUME_HANDLE}\" found. Stop backing up. Exit script.";
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  exit 0;
fi

## first check if volume to be backed up is present and unique 
if [ $(lvs | grep "${VOLUME_HANDLE}" | wc -l) -ne 1 ]
then
  msg="No logical volume named \"${VOLUME_HANDLE}\" found. Stop backing up. Exit script.";
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  echo "${msg}" >> "${errlogfile}";
  cleanup;
  exit 1;
fi

## try to retrieve belonging volume group
vg=$(lvs | grep "${VOLUME_HANDLE}" | awk '{print $2}')
if [ -z "$vg" ]
then
  msg="No volume group found for volume name \"${VOLUME_HANDLE}\". Stop backing up. Exit script.";
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  echo "${msg}" >> "${errlogfile}";
  cleanup;
  exit 1;
fi

## now create actual point-in-time cow snapshot
msg="$(lvcreate -l ${snapsize} -s -n ${snapname} /dev/${vg}/${VOLUME_HANDLE} 2>&1)";
if [ $? -ne 0 ]
then
  #msg="Could not create snapshot for \"${VOLUME_HANDLE}\" please see state msg of crd ${BACKUP_NAME}. Exit script.";
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  echo "${msg}" >> "${errlogfile}"
  cleanup;
  exit 1;
else
  logger -p local0.info -t "${lvmdtag}" "Snapshot created for ${BACKUP_NAME} \"${VOLUME_HANDLE}\"";
fi

## create snapshot mount point if not present
mkdir -p "${snapmp}";

## mount just created snapshot
if [ "$fstype" =  "xfs" ]
then
msg="$(mount -o nouuid -t ${fstype} /dev/${vg}/${snapname} ${snapmp} 2>&1)";
else
msg="$(mount -t ${fstype} /dev/${vg}/${snapname} ${snapmp} 2>&1)";
fi
if [ $? -ne 0 ]
then
  #logger -p local0.error -t ${lvmdtag} "Could not mount snapshot please see state msg of crd ${BACKUP_NAME}. Exit script.";
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  echo "${msg}" >> "${errlogfile}"
  cleanup;
  exit 1;
else
  logger -p local0.info -t "${lvmdtag}" "Snapshot mounted for ${BACKUP_NAME} \"${VOLUME_HANDLE}\"";
fi

## archive entire content from local mounted lv snapshot to destination
msg="$(tar -czf /tmp/${snapname}-$(date +%Y%m%d_%H%M%S).tar.gz -C ${snapmp} . 2>&1)";
if [ $? -ne 0 ]
then
  #logger -p local0.error -t ${lvmdtag} "Snapshot could not be archived to destination please see state msg of crd ${BACKUP_NAME}. Exit script.";
  logger -p local0.error -t "${lvmdtag}" "${msg}";
  echo "${msg}" >> "${errlogfile}"
  cleanup;
  exit 1;
else
  logger -p local0.info -t "${lvmdtag}" "Snapshot successfully archived for ${BACKUP_NAME} \"${VOLUME_HANDLE}\"";
  mkdir -p "${completedir}";
  cleanup;
fi




