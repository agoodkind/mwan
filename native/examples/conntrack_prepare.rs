use std::error::Error;
use std::io;
use std::mem::{size_of, zeroed};
use std::net::{Ipv4Addr, SocketAddrV4};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::process::ExitCode;

const CT_NEW: u16 = 256;
const CT_GET: u16 = 257;
const REQUEST_ACK: u16 = 5;
const NESTED: u16 = 0x8000;
const KIND_MASK: u16 = 0x3fff;
const BE_LIBERAL: u8 = 8;
const ESTABLISHED: u8 = 3;
type Result<T> = std::result::Result<T, Box<dyn Error>>;

fn require(condition: bool, message: &str) -> Result<()> {
    if condition {
        Ok(())
    } else {
        Err(io::Error::other(message).into())
    }
}
fn array<const SIZE: usize>(data: &[u8], offset: usize) -> Result<[u8; SIZE]> {
    data.get(offset..offset + SIZE)
        .and_then(|value| value.try_into().ok())
        .ok_or_else(|| io::Error::other("Incomplete kernel netlink record").into())
}
fn attribute(kind: u16, data: &[u8]) -> Vec<u8> {
    let length = data.len() + 4;
    let mut result = Vec::with_capacity((length + 3) & !3);
    result.extend_from_slice(&(length as u16).to_ne_bytes());
    result.extend_from_slice(&kind.to_ne_bytes());
    result.extend_from_slice(data);
    result.resize((length + 3) & !3, 0);
    result
}
fn child(data: &[u8], wanted: u16) -> Result<Option<&[u8]>> {
    let mut offset = 0;
    let mut found = None;
    while offset < data.len() {
        let length = usize::from(u16::from_ne_bytes(array(data, offset)?));
        let kind = u16::from_ne_bytes(array(data, offset + 2)?) & KIND_MASK;
        require(
            length >= 4 && offset + length <= data.len(),
            "Invalid kernel attribute length",
        )?;
        if kind == wanted {
            require(
                found.is_none(),
                "Kernel returned duplicate identity attributes",
            )?;
            found = Some(&data[offset + 4..offset + length]);
        }
        offset += (length + 3) & !3;
    }
    require(
        offset == data.len(),
        "Kernel attribute padding is incomplete",
    )?;
    Ok(found)
}
fn required(data: &[u8], kind: u16) -> Result<&[u8]> {
    child(data, kind)?
        .ok_or_else(|| io::Error::other(format!("Kernel omitted required attribute {kind}")).into())
}
fn identity(source: SocketAddrV4, destination: SocketAddrV4) -> Vec<u8> {
    let ip = [
        attribute(1, &source.ip().octets()),
        attribute(2, &destination.ip().octets()),
    ]
    .concat();
    let protocol = [
        attribute(1, &[6]),
        attribute(2, &source.port().to_be_bytes()),
        attribute(3, &destination.port().to_be_bytes()),
    ]
    .concat();
    attribute(
        1 | NESTED,
        &[attribute(1 | NESTED, &ip), attribute(2 | NESTED, &protocol)].concat(),
    )
}
fn tuple(data: &[u8]) -> Result<(SocketAddrV4, SocketAddrV4)> {
    let ip = required(data, 1)?;
    let protocol = required(data, 2)?;
    require(
        required(protocol, 1)? == [6],
        "Selected connection is not TCP",
    )?;
    let source = Ipv4Addr::from(array::<4>(required(ip, 1)?, 0)?);
    let destination = Ipv4Addr::from(array::<4>(required(ip, 2)?, 0)?);
    let source_port = u16::from_be_bytes(array(required(protocol, 2)?, 0)?);
    let destination_port = u16::from_be_bytes(array(required(protocol, 3)?, 0)?);
    Ok((
        SocketAddrV4::new(source, source_port),
        SocketAddrV4::new(destination, destination_port),
    ))
}
#[derive(Debug, PartialEq, Eq)]
struct Flow {
    id: u32,
    zone: u16,
    original: (SocketAddrV4, SocketAddrV4),
    reply: (SocketAddrV4, SocketAddrV4),
    state: u8,
    original_flags: u8,
    reply_flags: u8,
    original_scale: Option<u8>,
    reply_scale: Option<u8>,
}
impl Flow {
    fn decode(data: &[u8]) -> Result<Self> {
        let tcp = required(required(data, 4)?, 1)?;
        Ok(Self {
            id: u32::from_be_bytes(array(required(data, 12)?, 0)?),
            zone: match child(data, 18)? {
                Some(value) => u16::from_be_bytes(array(value, 0)?),
                None => 0,
            },
            original: tuple(required(data, 1)?)?,
            reply: tuple(required(data, 2)?)?,
            state: array::<1>(required(tcp, 1)?, 0)?[0],
            original_flags: array::<2>(required(tcp, 4)?, 0)?[0],
            reply_flags: array::<2>(required(tcp, 5)?, 0)?[0],
            original_scale: child(tcp, 2)?
                .map(|value| array::<1>(value, 0).map(|bytes| bytes[0]))
                .transpose()?,
            reply_scale: child(tcp, 3)?
                .map(|value| array::<1>(value, 0).map(|bytes| bytes[0]))
                .transpose()?,
        })
    }
    fn verify(
        &self,
        source: SocketAddrV4,
        destination: SocketAddrV4,
        id: Option<u32>,
    ) -> Result<()> {
        require(
            self.id != 0
                && self.zone == 0
                && self.original == (source, destination)
                && self.reply == (destination, source)
                && id.is_none_or(|id| self.id == id),
            "Conntrack identity, orientation, zone, or reply tuple changed",
        )
    }
    fn print(&self, stage: &str) {
        println!(
            "CONNTRACK stage={stage} id={} zone={} original={}->{} reply={}->{} state={} original_flags=0x{:02x} reply_flags=0x{:02x} original_scale={:?} reply_scale={:?}",
            self.id,
            self.zone,
            self.original.0,
            self.original.1,
            self.reply.0,
            self.reply.1,
            self.state,
            self.original_flags,
            self.reply_flags,
            self.original_scale,
            self.reply_scale
        );
    }
}
struct Kernel {
    socket: OwnedFd,
    sequence: u32,
}
impl Kernel {
    fn open() -> Result<Self> {
        // SAFETY: These constants request one kernel netfilter netlink socket.
        let descriptor = unsafe {
            libc::socket(
                libc::AF_NETLINK,
                libc::SOCK_RAW | libc::SOCK_CLOEXEC,
                libc::NETLINK_NETFILTER,
            )
        };
        if descriptor < 0 {
            return Err(io::Error::last_os_error().into());
        }
        // SAFETY: The successful socket call transferred this unique descriptor.
        let socket = unsafe { OwnedFd::from_raw_fd(descriptor) };
        // SAFETY: sockaddr_nl contains integer fields; zero initializes its padding and groups.
        let mut address: libc::sockaddr_nl = unsafe { zeroed() };
        address.nl_family = libc::AF_NETLINK as u16;
        // SAFETY: The complete address remains readable throughout bind.
        let result = unsafe {
            libc::bind(
                socket.as_raw_fd(),
                (&address as *const libc::sockaddr_nl).cast(),
                size_of::<libc::sockaddr_nl>() as libc::socklen_t,
            )
        };
        if result != 0 {
            return Err(io::Error::last_os_error().into());
        }
        let timeout = libc::timeval {
            tv_sec: 5,
            tv_usec: 0,
        };
        // SAFETY: The timeout is a complete readable timeval of the supplied size.
        let result = unsafe {
            libc::setsockopt(
                socket.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_RCVTIMEO,
                (&timeout as *const libc::timeval).cast(),
                size_of::<libc::timeval>() as libc::socklen_t,
            )
        };
        if result != 0 {
            return Err(io::Error::last_os_error().into());
        }
        Ok(Self {
            socket,
            sequence: 0,
        })
    }
    fn query(&mut self, kind: u16, attributes: &[u8]) -> Result<Option<Flow>> {
        self.sequence += 1;
        let mut message = Vec::new();
        message.extend_from_slice(&(20_u32 + attributes.len() as u32).to_ne_bytes());
        message.extend_from_slice(&kind.to_ne_bytes());
        message.extend_from_slice(&REQUEST_ACK.to_ne_bytes());
        message.extend_from_slice(&self.sequence.to_ne_bytes());
        message.extend_from_slice(&0_u32.to_ne_bytes());
        message.extend_from_slice(&[libc::AF_INET as u8, 0, 0, 0]);
        message.extend_from_slice(attributes);
        // SAFETY: Zero sets the kernel destination PID and multicast groups.
        let mut destination: libc::sockaddr_nl = unsafe { zeroed() };
        destination.nl_family = libc::AF_NETLINK as u16;
        // SAFETY: The owned socket and complete message and address remain live during send.
        let sent = unsafe {
            libc::sendto(
                self.socket.as_raw_fd(),
                message.as_ptr().cast(),
                message.len(),
                0,
                (&destination as *const libc::sockaddr_nl).cast(),
                size_of::<libc::sockaddr_nl>() as libc::socklen_t,
            )
        };
        if sent < 0 {
            return Err(io::Error::last_os_error().into());
        }
        require(
            sent as usize == message.len(),
            "Netlink request was incomplete",
        )?;
        let mut found = None;
        loop {
            let mut response = [0_u8; 65536];
            // SAFETY: Zero initializes the writable source-address record.
            let mut source: libc::sockaddr_nl = unsafe { zeroed() };
            let mut source_length = size_of::<libc::sockaddr_nl>() as libc::socklen_t;
            // SAFETY: Both output buffers and the length pointer have the supplied capacities.
            let received = unsafe {
                libc::recvfrom(
                    self.socket.as_raw_fd(),
                    response.as_mut_ptr().cast(),
                    response.len(),
                    libc::MSG_TRUNC,
                    (&mut source as *mut libc::sockaddr_nl).cast(),
                    &mut source_length,
                )
            };
            if received < 0 {
                return Err(io::Error::last_os_error().into());
            }
            require(
                received > 0 && received as usize <= response.len() && source.nl_pid == 0,
                "Netlink response is incomplete or did not come from the kernel",
            )?;
            let data = &response[..received as usize];
            let mut offset = 0;
            while offset < data.len() {
                let length = u32::from_ne_bytes(array(data, offset)?) as usize;
                let response_kind = u16::from_ne_bytes(array(data, offset + 4)?);
                let sequence = u32::from_ne_bytes(array(data, offset + 8)?);
                require(
                    length >= 16 && offset + length <= data.len() && sequence == self.sequence,
                    "Netlink response length or sequence is invalid",
                )?;
                let payload = &data[offset + 16..offset + length];
                if response_kind == libc::NLMSG_ERROR as u16 {
                    let error = i32::from_ne_bytes(array(payload, 0)?);
                    if error != 0 {
                        return Err(io::Error::from_raw_os_error(-error).into());
                    }
                    return Ok(found);
                }
                require(
                    response_kind == CT_NEW
                        && payload.len() >= 4
                        && payload[0] == libc::AF_INET as u8
                        && found.is_none(),
                    "Unexpected or duplicate conntrack response",
                )?;
                found = Some(Flow::decode(&payload[4..])?);
                offset += (length + 3) & !3;
            }
        }
    }
    fn get(&mut self, source: SocketAddrV4, destination: SocketAddrV4) -> Result<Flow> {
        self.query(CT_GET, &identity(source, destination))?
            .ok_or_else(|| io::Error::other("Kernel did not return the selected connection").into())
    }
}
fn run(arguments: &[String]) -> Result<()> {
    println!(
        "CONNTRACK namespace={}",
        std::fs::read_link("/proc/self/ns/net")?.display()
    );
    let source: SocketAddrV4 = arguments[2].parse()?;
    let destination: SocketAddrV4 = arguments[3].parse()?;
    require(
        source.port() != 0
            && destination.port() != 0
            && !source.ip().is_unspecified()
            && !destination.ip().is_unspecified(),
        "Specify complete original client and server endpoints",
    )?;
    let expected_id = if arguments[1] == "--set-liberal" {
        Some(arguments[4].parse::<u32>()?)
    } else {
        None
    };
    let mut kernel = Kernel::open()?;
    let before = kernel.get(source, destination)?;
    before.verify(source, destination, expected_id)?;
    before.print("before");
    if expected_id.is_none() {
        return Ok(());
    }
    require(
        before.state == ESTABLISHED,
        "Scoped preparation requires the existing established TCP connection",
    )?;
    let mut request = identity(source, destination);
    request.extend(attribute(12, &before.id.to_be_bytes()));
    request.extend(attribute(18, &before.zone.to_be_bytes()));
    let flags = [
        attribute(4, &[BE_LIBERAL, BE_LIBERAL]),
        attribute(5, &[BE_LIBERAL, BE_LIBERAL]),
    ]
    .concat();
    request.extend(attribute(4 | NESTED, &attribute(1 | NESTED, &flags)));
    require(
        kernel.query(CT_NEW, &request)?.is_none(),
        "Update returned an unexpected connection record",
    )?;
    let after = kernel.get(source, destination)?;
    after.verify(source, destination, Some(before.id))?;
    after.print("after");
    require(
        after.original_flags & BE_LIBERAL != 0
            && after.reply_flags & BE_LIBERAL != 0
            && after.state == before.state
            && after.original_scale == before.original_scale
            && after.reply_scale == before.reply_scale,
        "Scoped TCP preparation did not preserve state and window scales or set both liberal flags",
    )?;
    Ok(())
}
fn main() -> ExitCode {
    let arguments: Vec<String> = std::env::args().collect();
    let valid = arguments.len() == 4 && arguments[1] == "--inspect"
        || arguments.len() == 5 && arguments[1] == "--set-liberal";
    if !valid {
        eprintln!("Usage: conntrack_prepare --inspect ORIGINAL_CLIENT ORIGINAL_SERVER");
        eprintln!(
            "       conntrack_prepare --set-liberal ORIGINAL_CLIENT ORIGINAL_SERVER EXPECTED_ID"
        );
        return ExitCode::FAILURE;
    }
    match run(&arguments) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("Scoped conntrack operation failed: {error}");
            ExitCode::FAILURE
        }
    }
}
