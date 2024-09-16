# Testing on Stage

# Build the binaries
# make GO_REQUIRED_MIN_VERSION:= build

# scale down hive operator
oc scale deploy hive-operator -n multicluster-engine --replicas=0

# scale down hive controllers
oc scale -n hive deployment.v1.apps/hive-controllers --replicas=0

# run locally
# LOG_LEVEL="debug" HIVE_CLUSTERSYNC_POD_NAME="hive-clustersync-0 "HIVE_NS="hive"
# HIVE_CLUSTERSYNC_POD_NAME="hive-clustersync" HIVE_NS="hive" make GO_REQUIRED_MIN_VERSION:= run

HIVE_CLUSTERSYNC_POD_NAME="hive-clustersync-0" HIVE_NS="hive" ./bin/manager --controllers hibernation,clusterDeployment --log-level=debug | grep cluster-stakater-nonprod  | tee output.log





# oc -n cluster-stakater-nonprod patch cd stakater-nonprod --type='merge' -p $'spec:\n powerState: Hibernating'