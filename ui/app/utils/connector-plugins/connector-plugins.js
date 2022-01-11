import FileSource from './file/source';
import FileDestination from './file/destination';
import PostgresSource from './postgres/source';
import PostgresDestination from './postgres/destination';
import KafkaSource from './kafka/source';
import KafkaDestination from './kafka/destination';
import S3Destination from './s3/destination';
import S3Source from './s3/source';

export default [
  FileSource.toObject(),
  FileDestination.toObject(),
  PostgresSource.toObject(),
  PostgresDestination.toObject(),
  KafkaSource.toObject(),
  KafkaDestination.toObject(),
  S3Destination.toObject(),
  S3Source.toObject(),
];
