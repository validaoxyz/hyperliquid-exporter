import requests
import os
import json
import time
import logging
import subprocess
from prometheus_client import start_http_server, Counter, Gauge, Info
import threading
from dotenv import load_dotenv

# Load environment variables from .env file
load_dotenv()

# Get environment variables
NODE_HOME = os.getenv('NODE_HOME')
if not NODE_HOME:
    NODE_HOME = os.path.expanduser('~')
NODE_BINARY = os.getenv('NODE_BINARY')
if not NODE_BINARY:
    NODE_HOME = os.path.expanduser('~/hl-visor')
IS_VALIDATOR = os.getenv('IS_VALIDATOR', 'false').lower() == 'true'
VALIDATOR_ADDRESS = os.getenv('VALIDATOR_ADDRESS', '')

# Set up logging
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')

# Prometheus metrics
# For proposal counts: Use a single Counter with a label for the proposer address
hl_proposer_counter = Counter('proposer_count', 'Count of proposals by proposer', ['proposer'])

# For block metrics
hl_block_height_gauge = Gauge('hl_block_height', 'Block height from latest block time file')
hl_apply_duration_gauge = Gauge('hl_apply_duration', 'Apply duration from latest block time file')

# For jailed validators (updated to include 'name' label)
hl_validator_jailed_status = Gauge('hl_validator_jailed_status', 'Jailed status of validators', ['validator', 'name'])

# For total number of validators
hl_validator_count_gauge = Gauge('hl_validator_count', 'Total number of validators')

# For software version
hl_software_version_info = Info('hl_software_version', 'Software version information')

# Metric to indicate if software is up to date
hl_software_up_to_date = Gauge('hl_software_up_to_date', 'Indicates if the current software is up to date (1) or not (0)')

# Global variable to store the current commit hash
current_commit_hash = ''
validator_mapping = {}  # Mapping from shortened addresses to full addresses and names

def get_latest_file(directory):
    """Find the latest file in the given directory and its subdirectories."""
    latest_file = None
    latest_time = 0

    # Walk through the directory and its subdirectories
    for root, dirs, files in os.walk(directory):
        for file in files:
            file_path = os.path.join(root, file)
            try:
                file_mtime = os.path.getmtime(file_path)
                if file_mtime > latest_time:
                    latest_time = file_mtime
                    latest_file = file_path
            except Exception as e:
                logging.error(f"Error accessing file {file_path}: {e}")

    if latest_file is None:
        logging.warning(f"No files found in the directory {directory}.")
    return latest_file

def parse_log_line(line):
    """Parse a single log line and update the proposer count."""
    try:
        log_entry = json.loads(line)
        proposer = log_entry.get("abci_block", {}).get("proposer", None)

        if proposer:
            # Increment the proposer counter with the proposer label
            hl_proposer_counter.labels(proposer=proposer).inc()
            logging.info(f"Proposer {proposer} counter incremented.")
    except json.JSONDecodeError:
        logging.error(f"Error decoding JSON: {line}")
    except Exception as e:
        logging.error(f"Error processing line: {e}")

def stream_log_file(file_path, logs_dir, from_start=False):
    """Stream the log file continuously and switch to a new file if one appears.

    If from_start is True, start reading from the beginning of the file.
    """
    logging.info(f"Streaming log file: {file_path}, from_start={from_start}")
    with open(file_path, 'r') as log_file:
        if not from_start:
            log_file.seek(0, os.SEEK_END)  # Move to the end of the file only for the initial file

        while True:
            line = log_file.readline()
            if not line:
                # Check for new log file periodically
                latest_file = get_latest_file(logs_dir)
                if latest_file != file_path:
                    logging.info(f"Switching to new log file: {latest_file}")
                    return latest_file  # Return the new file to switch

                # If no new log file, wait for new lines to be written
                time.sleep(1)
                continue

            # Process the line and update Prometheus metrics
            parse_log_line(line)

def proposal_count_monitor():
    logs_dir = os.path.join(NODE_HOME, "hl/data/replica_cmds")

    # Get the initial latest log file and start streaming from the end
    latest_file = get_latest_file(logs_dir)
    first_run = True

    while True:
        if latest_file:
            logging.info(f"Found latest log file: {latest_file}")
            try:
                # For the first run, start from the end (from_start=False)
                from_start = not first_run
                new_file = stream_log_file(latest_file, logs_dir, from_start=from_start)
                if new_file:
                    latest_file = new_file  # Switch to the new file
                    first_run = False  # After first run, subsequent files will be read from the start
            except Exception as e:
                logging.error(f"Error while streaming the file {latest_file}: {e}")
        else:
            logging.info("No log files found. Retrying in 10 seconds...")

        # Sleep for 10 seconds before checking for the latest file again
        time.sleep(10)

def parse_block_time_line(line):
    """Parse a block time line and update Prometheus metrics."""
    try:
        data = json.loads(line)
        # Extract height as integer, and apply_duration as float
        block_height = data.get('height', None)
        block_time = data.get('block_time', None)  # Keep block_time as string, print it only
        apply_duration = data.get('apply_duration', None)

        # Convert block height to integer if available
        if block_height is not None:
            hl_block_height_gauge.set(int(block_height))  # Set block height as integer

        # Set apply_duration if available (this should be a float)
        if apply_duration is not None:
            hl_apply_duration_gauge.set(float(apply_duration))

        # Print block_time and apply_duration as strings
        logging.info(f"Updated metrics: height={block_height}, block_time={block_time}, apply_duration={apply_duration}")
    except json.JSONDecodeError:
        logging.error(f"Error parsing line: {line}")
    except Exception as e:
        logging.error(f"Error updating metrics: {e}")

def stream_block_time_file(file_path, logs_dir, from_start=False):
    """Stream the block time file and update metrics."""
    logging.info(f"Streaming block time file: {file_path}, from_start={from_start}")
    with open(file_path, 'r') as log_file:
        if not from_start:
            log_file.seek(0, os.SEEK_END)  # Move to the end of the file only for the initial file

        while True:
            line = log_file.readline()
            if not line:
                # Check for new file periodically
                latest_file = get_latest_file(logs_dir)
                if latest_file != file_path:
                    logging.info(f"Switching to new block time file: {latest_file}")
                    return latest_file  # Return the new file to switch

                # If no new file, wait for new lines to be written
                time.sleep(1)
                continue

            # Process the line and update Prometheus metrics
            parse_block_time_line(line)

def block_time_monitor():
    block_time_dir = os.path.join(NODE_HOME, 'hl/data/block_times')

    # Get the initial latest file and start streaming from the end
    latest_file = get_latest_file(block_time_dir)
    first_run = True

    while True:
        if latest_file:
            logging.info(f"Found latest block time file: {latest_file}")
            try:
                # For the first run, start from the end (from_start=False)
                from_start = not first_run
                new_file = stream_block_time_file(latest_file, block_time_dir, from_start=from_start)
                if new_file:
                    latest_file = new_file  # Switch to the new file
                    first_run = False
            except Exception as e:
                logging.error(f"Error while streaming block time file {latest_file}: {e}")
        else:
            logging.info("No block time files found. Retrying in 5 seconds...")

        # Sleep for 5 seconds before checking for the latest file again
        time.sleep(5)


def update_validator_mapping():
    """Fetch validator summaries and update the validator mapping."""
    global validator_mapping
    while True:
        try:
            logging.info("Fetching validator summaries...")
            url = 'https://api.hyperliquid-testnet.xyz/info'
            headers = {'Content-Type': 'application/json'}
            data = json.dumps({"type": "validatorSummaries"})
            response = requests.post(url, headers=headers, data=data, timeout=10)
            response.raise_for_status()
            validator_summaries = response.json()
            new_mapping = {}
            for summary in validator_summaries:
                full_address = summary['validator']
                name = summary.get('name', 'Unknown')
                # Create a shortened address similar to the one in the logs
                shortened_address = f"{full_address[:6]}..{full_address[-4:]}"
                new_mapping[shortened_address] = {'full_address': full_address, 'name': name}
            validator_mapping = new_mapping
            # Update the validator count metric
            hl_validator_count_gauge.set(len(validator_summaries))
            logging.info(f"Validator mapping updated. Total validators: {len(validator_summaries)}")
        except Exception as e:
            logging.error(f"Error fetching validator summaries: {e}")
        # Wait for 10 minutes before next update
        time.sleep(600)


def parse_consensus_log_line(line):
    """Parse a consensus log line and update the jailed validators metric."""
    global validator_mapping
    try:
        data = json.loads(line)
        # Extract jailed_validators
        jailed_validators = data[1][1].get('jailed_validators', [])
        # Extract all validators from 'round_to_stakes'
        round_to_stakes = data[1][1].get('execution_state', {}).get('round_to_stakes', [])
        all_validators = set()

        for round_entry in round_to_stakes:
            validators_list = round_entry[1]  # Should be the list of [validator, stake]
            for validator_entry in validators_list:
                validator_short = validator_entry[0]
                all_validators.add(validator_short)

        # Update the Prometheus metric
        for validator_short in all_validators:
            mapping_entry = validator_mapping.get(validator_short, {})
            full_address = mapping_entry.get('full_address', validator_short)
            name = mapping_entry.get('name', 'Unknown')
            is_jailed = 1 if validator_short in jailed_validators else 0
            hl_validator_jailed_status.labels(validator=full_address, name=name).set(is_jailed)
            status_str = "jailed" if is_jailed else "not jailed"
            logging.info(f"Validator {full_address} ({name}) is {status_str}.")
    except Exception as e:
        logging.error(f"Error parsing consensus log line: {e}")


def stream_consensus_log_file(file_path, logs_dir, from_start=False):
    """Stream the consensus log file and update jailed validator metrics."""
    logging.info(f"Streaming consensus log file: {file_path}, from_start={from_start}")
    with open(file_path, 'r') as log_file:
        if not from_start:
            log_file.seek(0, os.SEEK_END)  # Move to the end of the file only for the initial file

        while True:
            line = log_file.readline()
            if not line:
                # Check for new log file periodically
                latest_file = get_latest_file(logs_dir)
                if latest_file != file_path:
                    logging.info(f"Switching to new consensus log file: {latest_file}")
                    return latest_file  # Return the new file to switch

                # If no new log file, wait for new lines to be written
                time.sleep(1)
                continue

            # Process the line and update Prometheus metrics
            parse_consensus_log_line(line)

def consensus_log_file_monitor():
    """Monitor the consensus log file for jailed validator updates."""
    consensus_dir = os.path.join(NODE_HOME, f"hl/data/consensus{VALIDATOR_ADDRESS}")

    if not os.path.exists(consensus_dir):
        logging.error(f"Consensus directory {consensus_dir} does not exist. Are you sure you're a validator?")
        return

    # Initialize first_run outside the loop
    first_run = True

    while True:
        latest_file = get_latest_file(consensus_dir)
        if latest_file:
            logging.info(f"Found latest consensus file: {latest_file}")
            try:
                # For the first run, start from the end (from_start=False)
                from_start = not first_run
                new_file = stream_consensus_log_file(latest_file, consensus_dir, from_start=from_start)
                if new_file:
                    latest_file = new_file  # Switch to the new file
                    first_run = False
            except Exception as e:
                logging.error(f"Error while streaming consensus file {latest_file}: {e}")
        else:
            logging.info("No consensus log files found. Retrying in 10 seconds...")

        # Sleep for 10 seconds before checking for the latest file again
        time.sleep(10)

def software_version_monitor():
    """Monitor the software version and update the metric."""
    global current_commit_hash
    while True:
        try:
            # Run the command
            result = subprocess.run([NODE_BINARY, '--version'], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            version_output = result.stdout.decode('utf-8').strip()
            # Parse the version output
            parts = version_output.split('|')
            if len(parts) >= 3:
                commit_line = parts[0]
                date = parts[1]
                uncommitted_status = parts[2]

                commit_parts = commit_line.split(' ')
                if len(commit_parts) >= 2:
                    commit_hash = commit_parts[1]
                else:
                    commit_hash = ''

                current_commit_hash = commit_hash  # Update the global variable

                # Update the Prometheus Info metric with labels
                hl_software_version_info.info({'commit': commit_hash, 'date': date})
                logging.info(f"Updated software version: commit={commit_hash}, date={date}")
            else:
                logging.error(f"Unexpected version output format: {version_output}")
        except Exception as e:
            logging.error(f"Error getting software version: {e}")

        time.sleep(60)  # Check every 60 seconds

def check_software_update():
    """Check if the current software is up to date with the latest binary."""
    url = 'https://binaries.hyperliquid.xyz/Testnet/hl-visor'
    local_latest_binary = '/tmp/hl-visor-latest'

    global current_commit_hash  # Access the global variable

    while True:
        try:
            # Download the latest binary
            logging.info("Downloading the latest binary...")
            result = subprocess.run(['curl', '-sSL', '-o', local_latest_binary, url], check=True)
            # Make it executable
            os.chmod(local_latest_binary, 0o755)

            # Run the latest binary with --version
            result = subprocess.run([local_latest_binary, '--version'], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            latest_version_output = result.stdout.decode('utf-8').strip()

            # Parse the latest version output
            parts = latest_version_output.split('|')
            if len(parts) >= 3:
                commit_line = parts[0]
                latest_date = parts[1]
                uncommitted_status = parts[2]

                commit_parts = commit_line.split(' ')
                if len(commit_parts) >= 2:
                    latest_commit_hash = commit_parts[1]
                else:
                    latest_commit_hash = ''

                # Compare the commit hashes
                if current_commit_hash == '':
                    logging.warning("Current commit hash is not available yet.")
                else:
                    if current_commit_hash == latest_commit_hash:
                        # Software is up to date
                        hl_software_up_to_date.set(1)
                        logging.info("Software is up to date.")
                    else:
                        # Software is not up to date
                        hl_software_up_to_date.set(0)
                        logging.info("Software is NOT up to date.")

            else:
                logging.error(f"Unexpected latest version output format: {latest_version_output}")

        except Exception as e:
            logging.error(f"Error checking software update: {e}")

        # Wait for a certain interval before checking again
        time.sleep(300)  # Check every 5 minutes

if __name__ == "__main__":
    # Start Prometheus HTTP server on port 8086
    logging.info("Starting Prometheus HTTP server on port 8086")
    start_http_server(8086)
    logging.info("Prometheus HTTP server started on port 8086")

    # Start the proposal count monitor in a separate thread
    proposal_thread = threading.Thread(target=proposal_count_monitor)
    proposal_thread.daemon = True
    proposal_thread.start()
    logging.info("Started proposal count monitoring thread.")

    # Start the block time monitor in a separate thread
    block_time_thread = threading.Thread(target=block_time_monitor)
    block_time_thread.daemon = True
    block_time_thread.start()
    logging.info("Started block time monitoring thread.")

    # Start the consensus log monitor in a separate thread if IS_VALIDATOR is true
    if IS_VALIDATOR:
        if not VALIDATOR_ADDRESS:
            logging.error("VALIDATOR_ADDRESS is not set. Cannot start consensus log monitor.")
        else:
            consensus_thread = threading.Thread(target=consensus_log_file_monitor)
            consensus_thread.daemon = True
            consensus_thread.start()
            logging.info("Started consensus log monitoring thread.")
    else:
        logging.info("IS_VALIDATOR is false. Skipping consensus log monitoring.")

    # Start the software version monitor in a separate thread
    software_version_thread = threading.Thread(target=software_version_monitor)
    software_version_thread.daemon = True
    software_version_thread.start()
    logging.info("Started software version monitoring thread.")

    # Start the software update checker in a separate thread
    software_update_thread = threading.Thread(target=check_software_update)
    software_update_thread.daemon = True
    software_update_thread.start()
    logging.info("Started software update checking thread.")

    # Start the validator mapping updater in a separate thread
    validator_mapping_thread = threading.Thread(target=update_validator_mapping)
    validator_mapping_thread.daemon = True
    validator_mapping_thread.start()
    logging.info("Started validator mapping updater thread.")

    # Keep the main thread alive
    while True:
        time.sleep(1)

